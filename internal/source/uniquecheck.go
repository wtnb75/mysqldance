package source

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"

	"mysqldance/internal/extsort"
	"mysqldance/internal/mask"
	"mysqldance/internal/sqlvalue"
)

// hashLen is the number of bytes of the SHA-256 of a key that are compared. Two
// different keys that share them (about 2^-128 for each pair of rows) are
// reported as a collision, the side the error falls on, so it is ignored.
const hashLen = 16

// uniqueChecker finds the rows of a table that are equal on a unique index after
// the rows were masked (a transform can give two rows one value). For each index
// it keeps a sorter of the hashes of the keys of the rows, and compares
// neighbours once the table is read. Only hashes are held, in memory or in a
// scratch file: neither a value read nor a value masked is.
type uniqueChecker struct {
	checks  []mask.UniqueCheck
	sorters []*extsort.Sorter
	buf     []byte
}

func newUniqueChecker(checks []mask.UniqueCheck, memory int64, newFile extsort.NewFile) *uniqueChecker {
	u := &uniqueChecker{checks: checks}
	for range checks {
		u.sorters = append(u.sorters, extsort.New(memory, newFile))
	}
	return u
}

// add records the key of row on every index. A row with a NULL in a column of an
// index is not recorded for it: a UNIQUE index lets rows with a NULL repeat.
func (u *uniqueChecker) add(row []sqlvalue.Value) error {
	for i, c := range u.checks {
		key, ok := u.key(c, row)
		if !ok {
			continue
		}
		if err := u.sorters[i].Add(key, nil); err != nil {
			return err
		}
	}
	return nil
}

// key hashes the columns of the index: for each, the length of the bytes and the
// bytes (ASCII letters in lower case for a column that ignores case), so that the
// boundaries between the columns are part of what is hashed.
func (u *uniqueChecker) key(c mask.UniqueCheck, row []sqlvalue.Value) ([]byte, bool) {
	u.buf = u.buf[:0]
	for j, pos := range c.Positions {
		v := row[pos]
		if v.IsNull() {
			return nil, false
		}
		b := v.Bytes()
		u.buf = binary.AppendUvarint(u.buf, uint64(len(b)))
		start := len(u.buf)
		u.buf = append(u.buf, b...)
		if c.Fold[j] {
			foldASCII(u.buf[start:])
		}
	}
	sum := sha256.Sum256(u.buf)
	return sum[:hashLen], true
}

// foldASCII folds only A-Z, as the spec of B-168 decides; non-ASCII case, accents
// and PAD SPACE are not detected (README, review ledger R-231).
func foldASCII(b []byte) {
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 'a' - 'A'
		}
	}
}

// verify fails if two rows are equal on an index. The error names the table, the
// index and its columns, and the number of rows that have another row equal to
// them (not the number of pairs); never a value, a position or a key.
func (u *uniqueChecker) verify(db, table string) error {
	var errs []error
	for i, c := range u.checks {
		var prev []byte
		run, dup := 0, 0
		err := u.sorters[i].Each(func(key, _ []byte) error {
			if prev != nil && bytes.Equal(prev, key) {
				run++
				if run == 2 {
					dup += 2
				} else {
					dup++
				}
				return nil
			}
			prev, run = append(prev[:0], key...), 1
			return nil
		})
		if err != nil {
			return err
		}
		if dup > 0 {
			names := make([]string, len(c.Columns))
			for j, n := range c.Columns {
				names[j] = sqlvalue.QuoteIdent(n)
			}
			errs = append(errs, fmt.Errorf("masking made %d rows of %s.%s equal on the unique index %q (columns %s), so they would collide when restored: choose another transform for these columns, or exclude the table",
				dup, sqlvalue.QuoteIdent(db), sqlvalue.QuoteIdent(table), c.Index, strings.Join(names, ", ")))
		}
	}
	return errors.Join(errs...)
}

func (u *uniqueChecker) close() error {
	var errs []error
	for _, s := range u.sorters {
		errs = append(errs, s.Close())
	}
	return errors.Join(errs...)
}
