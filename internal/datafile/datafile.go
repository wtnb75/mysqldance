// Package datafile writes the per-table data file: a fixed header followed by
// INSERT statements with one row per line, chunked by row content.
package datafile

import (
	"bufio"
	"hash/fnv"
	"io"
	"strings"
)

const (
	defaultRowsPerInsert  = 1000
	defaultMaxInsertBytes = 1048576
)

// Options controls how rows are grouped into INSERT statements.
type Options struct {
	// RowsPerInsert is the average number of rows per statement (default 1000).
	RowsPerInsert uint32
	// MaxInsertBytes is a safety cap on statement size (default 1048576).
	MaxInsertBytes int
	// RowsPerFile is the average number of rows per data file when a table is
	// split into several files (see Split). 0 writes every table to one file.
	RowsPerFile uint32
}

func quoteIdent(s string) string {
	return "`" + strings.ReplaceAll(s, "`", "``") + "`"
}

// Header returns the fixed preamble of every dump file for the given database:
// HeaderSettings followed by HeaderUse.
func Header(db string) string {
	return HeaderSettings() + HeaderUse(db)
}

// HeaderSettings returns the session settings of the header. They come first
// in every file, so SET NAMES precedes any non-ASCII name (the schema file
// puts CREATE DATABASE between this part and HeaderUse).
func HeaderSettings() string {
	return "SET NAMES utf8mb4;\n" +
		"SET TIME_ZONE='+00:00';\n" +
		"SET SQL_MODE='NO_AUTO_VALUE_ON_ZERO';\n" +
		"SET FOREIGN_KEY_CHECKS=0;\n" +
		"SET UNIQUE_CHECKS=0;\n"
}

// HeaderUse returns the USE statement that ends the header.
func HeaderUse(db string) string {
	return "USE " + quoteIdent(db) + ";\n"
}

// Split divides the rows of one table, already in output order, into the
// groups that become separate data files. A group ends after a row whose
// key hash modulo rowsPerFile is zero, and after the last row. keyCols are the
// indexes of the key columns in a row; the hash is the FNV-1a 32-bit hash of
// those values, so updating a non-key column never moves a boundary. Without
// key columns (a table without a usable key) the whole row is hashed instead.
// Content-defined cuts make the sizes vary (rowsPerFile is only an average)
// and a table may turn from one group into several as it grows; both are
// intended (review ledger R-026, R-027). rowsPerFile 0 returns one group. The result is never empty: a table without
// rows is one empty group, which becomes a file holding only the header.
func Split(rows [][]string, keyCols []int, rowsPerFile uint32) [][][]string {
	if rowsPerFile == 0 || len(rows) == 0 {
		return [][][]string{rows}
	}
	var groups [][][]string
	start := 0
	for i, row := range rows {
		if keyHash(row, keyCols)%rowsPerFile == 0 {
			groups = append(groups, rows[start:i+1])
			start = i + 1
		}
	}
	if start < len(rows) {
		groups = append(groups, rows[start:])
	}
	return groups
}

func keyHash(row []string, keyCols []int) uint32 {
	h := fnv.New32a()
	if len(keyCols) == 0 {
		_, _ = h.Write([]byte(strings.Join(row, ",")))
		return mix32(h.Sum32())
	}
	for _, c := range keyCols {
		// The separator keeps ("1,2", "3") apart from ("1", "2,3").
		_, _ = h.Write([]byte(row[c]))
		_, _ = h.Write([]byte{0})
	}
	return mix32(h.Sum32())
}

// mix32 is the MurmurHash3 finalizer. The low bits of FNV-1a depend only on
// the low bits of the input bytes, so keys with a common suffix would almost
// never be a multiple of an even N; mixing spreads every input bit over the
// result.
func mix32(h uint32) uint32 {
	h ^= h >> 16
	h *= 0x85ebca6b
	h ^= h >> 13
	h *= 0xc2b2ae35
	h ^= h >> 16
	return h
}

// Write writes the header and the rows of one table as INSERT statements.
//
// Statement boundaries are content-defined: a statement is closed after a row
// whose FNV-1a 32-bit hash modulo RowsPerInsert is zero, after the row that
// makes the statement reach MaxInsertBytes, and after the last row. Removing
// one row therefore only affects boundaries next to it.
func Write(w io.Writer, db, table string, columns []string, listColumns bool, rows [][]string, opt Options) error {
	if opt.RowsPerInsert == 0 {
		opt.RowsPerInsert = defaultRowsPerInsert
	}
	if opt.MaxInsertBytes <= 0 {
		opt.MaxInsertBytes = defaultMaxInsertBytes
	}

	bw := bufio.NewWriter(w)
	if _, err := bw.WriteString(Header(db)); err != nil {
		return err
	}

	var head strings.Builder
	head.WriteString("INSERT INTO " + quoteIdent(table))
	if listColumns {
		quoted := make([]string, len(columns))
		for i, c := range columns {
			quoted[i] = quoteIdent(c)
		}
		head.WriteString(" (" + strings.Join(quoted, ", ") + ")")
	}
	head.WriteString(" VALUES\n")

	open := false
	size := 0
	for i, row := range rows {
		if !open {
			if _, err := bw.WriteString(head.String()); err != nil {
				return err
			}
			size = head.Len()
			open = true
		}
		text := "(" + strings.Join(row, ",") + ")"
		h := fnv.New32a()
		_, _ = h.Write([]byte(text))
		size += len(text) + 2
		closeNow := i == len(rows)-1 || h.Sum32()%opt.RowsPerInsert == 0 || size >= opt.MaxInsertBytes
		sep := ",\n"
		if closeNow {
			sep = ";\n"
		}
		if _, err := bw.WriteString(text + sep); err != nil {
			return err
		}
		if closeNow {
			open = false
		}
	}
	return bw.Flush()
}
