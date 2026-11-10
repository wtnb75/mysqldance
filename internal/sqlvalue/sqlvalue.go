// Package sqlvalue formats values read from MySQL as SQL literals.
package sqlvalue

import (
	"math/big"
	"strings"
)

// Column describes a source column.
type Column struct {
	Name string
	// TypeName is database/sql's ColumnType.DatabaseTypeName().
	TypeName string
}

// Value is one cell read from the source: the column's type name, the bytes the
// MySQL text protocol delivered, and whether the cell is NULL. It carries no
// dialect; Literal renders it for MySQL, and writers for other DBMSs can render
// it their own way. The fields are private so that only NewValue builds one
// (review ledger R-047).
type Value struct {
	typeName string
	raw      []byte
	null     bool
}

// NewValue returns a Value holding a copy of raw, because database/sql reuses
// the buffer behind sql.RawBytes on the next Scan. A non-NULL value keeps
// raw != nil so an empty string stays distinct from NULL.
func NewValue(typeName string, raw []byte, isNull bool) Value {
	if isNull {
		return Value{typeName: typeName, null: true}
	}
	return Value{typeName: typeName, raw: append([]byte{}, raw...)}
}

// Type returns database/sql's DatabaseTypeName of the column.
func (v Value) Type() string { return v.typeName }

// IsNull reports whether the cell is NULL.
func (v Value) IsNull() bool { return v.null }

// Bytes returns the cell as the server formatted it, or nil for NULL. The
// caller must not modify it.
func (v Value) Bytes() []byte { return v.raw }

// Literal formats v as a MySQL SQL literal.
func (v Value) Literal() string { return Literal(v.typeName, v.raw, v.null) }

// Literals formats each value of row as a MySQL SQL literal.
func Literals(row []Value) []string {
	out := make([]string, len(row))
	for i, v := range row {
		out[i] = v.Literal()
	}
	return out
}

const hexDigits = "0123456789ABCDEF"

// QuoteIdent quotes a MySQL identifier, doubling embedded backticks.
func QuoteIdent(s string) string {
	return "`" + strings.ReplaceAll(s, "`", "``") + "`"
}

// Literal formats raw (as returned by the MySQL text protocol) as a SQL literal.
func Literal(typeName string, raw []byte, isNull bool) string {
	if isNull {
		return "NULL"
	}
	t := strings.ToUpper(strings.TrimSpace(typeName))
	t = strings.TrimPrefix(t, "UNSIGNED ")
	switch t {
	case "TINYINT", "SMALLINT", "MEDIUMINT", "INT", "INTEGER", "BIGINT",
		"DECIMAL", "NUMERIC", "FLOAT", "DOUBLE", "YEAR":
		return string(raw)
	case "BIT":
		return bitLiteral(raw)
	case "TINYBLOB", "BLOB", "MEDIUMBLOB", "LONGBLOB", "BINARY", "VARBINARY", "GEOMETRY":
		return hexLiteral(raw)
	}
	return quote(raw)
}

func bitLiteral(raw []byte) string {
	bits := new(big.Int).SetBytes(raw).Text(2)
	return "b'" + bits + "'"
}

func hexLiteral(raw []byte) string {
	if len(raw) == 0 {
		return "''"
	}
	var b strings.Builder
	b.Grow(2 + 2*len(raw))
	b.WriteString("0x")
	for _, c := range raw {
		b.WriteByte(hexDigits[c>>4])
		b.WriteByte(hexDigits[c&0x0f])
	}
	return b.String()
}

// quote escapes raw like mysql_real_escape_string, byte by byte.
func quote(raw []byte) string {
	var b strings.Builder
	b.Grow(len(raw) + 2)
	b.WriteByte('\'')
	for _, c := range raw {
		switch c {
		case 0:
			b.WriteString(`\0`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\\':
			b.WriteString(`\\`)
		case '\'':
			b.WriteString(`\'`)
		case '"':
			b.WriteString(`\"`)
		case 0x1a:
			b.WriteString(`\Z`)
		default:
			b.WriteByte(c)
		}
	}
	b.WriteByte('\'')
	return b.String()
}
