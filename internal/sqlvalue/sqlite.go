package sqlvalue

import (
	"bytes"
	"math/big"
	"strings"
)

// SQLiteLiteral formats raw (as returned by the MySQL text protocol) as a
// SQLite literal.
//
// SQLite has no backslash escapes and no BIT or DECIMAL storage, so the
// rendering differs from Literal: integers that do not fit in 64 bits and
// DECIMAL values are written as text strings (a bare literal would become a
// lossy REAL), binary values as X'..', and a text value holding a line break
// as a concatenation with char(10) / char(13), which keeps one record on one
// line.
func SQLiteLiteral(typeName string, raw []byte, isNull bool) string {
	if isNull {
		return "NULL"
	}
	t := strings.ToUpper(strings.TrimSpace(typeName))
	unsigned := strings.HasPrefix(t, "UNSIGNED ")
	t = strings.TrimPrefix(t, "UNSIGNED ")
	switch t {
	case "TINYINT", "SMALLINT", "MEDIUMINT", "INT", "INTEGER", "YEAR", "FLOAT", "DOUBLE":
		return string(raw)
	case "BIGINT":
		if unsigned {
			return sqliteQuote(raw)
		}
		return string(raw)
	case "DECIMAL", "NUMERIC":
		return sqliteQuote(raw)
	case "BIT":
		n := new(big.Int).SetBytes(raw)
		if n.IsInt64() {
			return n.String()
		}
		return "'" + n.String() + "'"
	case "TINYBLOB", "BLOB", "MEDIUMBLOB", "LONGBLOB", "BINARY", "VARBINARY", "GEOMETRY":
		return sqliteBlob(raw)
	}
	return sqliteText(raw)
}

// SQLiteLiterals formats each value of row as a SQLite literal.
func SQLiteLiterals(row []Value) []string {
	out := make([]string, len(row))
	for i, v := range row {
		out[i] = SQLiteLiteral(v.typeName, v.raw, v.null)
	}
	return out
}

func sqliteBlob(raw []byte) string { return "X'" + hexBody(raw) + "'" }

func sqliteQuote(raw []byte) string {
	return "'" + strings.ReplaceAll(string(raw), "'", "''") + "'"
}

// lineBreaks turns a line break inside a quoted string into a concatenation
// with char(); an empty quoted piece next to a break is valid SQL.
var lineBreaks = strings.NewReplacer("\n", "'||char(10)||'", "\r", "'||char(13)||'")

// sqliteText renders a text value. A NUL byte cannot appear in a SQLite string
// literal, so such a value goes through a BLOB cast; line breaks are written
// as char() calls.
func sqliteText(raw []byte) string {
	breaks := bytes.Count(raw, []byte{'\n'}) + bytes.Count(raw, []byte{'\r'})
	// Every || deepens the expression tree, and SQLite refuses a depth over
	// 1000, so a value with many breaks is written as hex instead.
	if bytes.IndexByte(raw, 0) >= 0 || breaks > maxConcatBreaks {
		return "CAST(" + sqliteBlob(raw) + " AS TEXT)"
	}
	if breaks > 0 {
		return "(" + lineBreaks.Replace(sqliteQuote(raw)) + ")"
	}
	return sqliteQuote(raw)
}

// maxConcatBreaks is the most line breaks a text value may hold and still be
// written as a concatenation; each break adds two || operators.
const maxConcatBreaks = 100
