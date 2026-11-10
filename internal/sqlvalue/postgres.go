package sqlvalue

import (
	"bytes"
	"fmt"
	"math/big"
	"regexp"
	"strings"
	"time"
)

// PGKind says how a PostgreSQL column takes its values.
type PGKind int

const (
	// PGNumber is any integer, numeric or float column: the digits the server
	// returned are valid as they are.
	PGNumber PGKind = iota
	// PGBool is a boolean column made from TINYINT(1).
	PGBool
	// PGBit is a bit(n) column; PGColumn.Bits is n.
	PGBit
	// PGText is a character or JSON column (text, varchar, json).
	PGText
	// PGBytea is a binary or spatial column.
	PGBytea
	// PGDate is a date column.
	PGDate
	// PGTimestamp is a timestamp or timestamptz column (from DATETIME or
	// TIMESTAMP). The session time zone is UTC, as in the dump.
	PGTimestamp
	// PGTime is an interval column (from TIME, whose range is wider than the
	// time of day).
	PGTime
)

// PGColumn describes the PostgreSQL column a value goes into.
type PGColumn struct {
	Kind PGKind
	// Bits is the width of a PGBit column.
	Bits int
}

// QuotePGIdent quotes a PostgreSQL identifier, doubling embedded double quotes.
func QuotePGIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// PostgresLiteral formats raw (as returned by the MySQL text protocol) as a
// PostgreSQL literal for column c. The session must have
// standard_conforming_strings on, so that a backslash in '...' is literal.
//
// A value PostgreSQL cannot hold (a NUL byte in text, a zero, partial or year-0 date,
// a TINYINT(1) other than 0 and 1) is an error: the dump must not change it or
// leave it out.
//
// A text value holding a line break is written as an E'...' string with \n and
// \r escapes, which keeps one record on one line.
func PostgresLiteral(c PGColumn, raw []byte, isNull bool) (string, error) {
	if isNull {
		return "NULL", nil
	}
	switch c.Kind {
	case PGNumber:
		return string(raw), nil
	case PGBool:
		switch string(raw) {
		case "0":
			return "false", nil
		case "1":
			return "true", nil
		}
		return "", fmt.Errorf("value %q of a TINYINT(1) column is not 0 or 1, which PostgreSQL boolean cannot hold", raw)
	case PGBit:
		n := new(big.Int).SetBytes(raw)
		if c.Bits <= 0 || n.BitLen() > c.Bits {
			return "", fmt.Errorf("value 0x%X does not fit BIT(%d)", raw, c.Bits)
		}
		digits := n.Text(2)
		return "B'" + strings.Repeat("0", c.Bits-len(digits)) + digits + "'", nil
	case PGBytea:
		return `'\x` + hexBody(raw) + "'", nil
	case PGDate:
		if d, err := time.Parse("2006-01-02", string(raw)); err != nil || d.Year() < 1 {
			return "", fmt.Errorf("%q is not a date PostgreSQL accepts", raw)
		}
		return "'" + string(raw) + "'", nil
	case PGTimestamp:
		if d, err := time.Parse("2006-01-02 15:04:05.999999", string(raw)); err != nil || d.Year() < 1 {
			return "", fmt.Errorf("%q is not a date and time PostgreSQL accepts", raw)
		}
		return "'" + string(raw) + "'", nil
	case PGTime:
		if !mysqlTime.Match(raw) {
			return "", fmt.Errorf("%q is not a time of the form [-]H:MM:SS[.ffffff]", raw)
		}
		return "'" + string(raw) + "'", nil
	}
	return pgText(raw)
}

var mysqlTime = regexp.MustCompile(`^-?\d{1,3}:[0-5]\d:[0-5]\d(\.\d{1,6})?$`)

func pgText(raw []byte) (string, error) {
	if bytes.IndexByte(raw, 0) >= 0 {
		return "", fmt.Errorf("a text value holds a NUL byte, which PostgreSQL cannot store")
	}
	s := strings.ReplaceAll(string(raw), "'", "''")
	if !strings.ContainsAny(s, "\n\r") {
		return "'" + s + "'", nil
	}
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, "\n", `\n`)
	s = strings.ReplaceAll(s, "\r", `\r`)
	return "E'" + s + "'", nil
}
