package mask

import (
	"crypto/hmac"
	"crypto/sha256"
	"strconv"

	"mysqldance/internal/sqlvalue"
)

// Column is the definition of a column as a transform sees it: what the server
// reports for it, and where its value is in a row.
type Column struct {
	Name string
	// TypeName is the driver's type name (VARCHAR, INT, ...), ColumnType the
	// catalog's COLUMN_TYPE (varchar(20), decimal(10,2), int unsigned) and
	// DataType its DATA_TYPE (varchar, decimal, int).
	TypeName, ColumnType, DataType string
	// CharacterSet is the column's character set, or empty for a column that has
	// none.
	CharacterSet string
	Nullable     bool
	// Index is the position of the column in a row (the rows hold the columns
	// that are not generated, in ordinal order).
	Index int
}

// BuildContext is what a transform needs to prepare itself for one rule. It is
// built once per rule, before the dump, and so is the Func it returns.
type BuildContext struct {
	// Args are the arguments of the rule, as Parse read them: a string, an int64, a
	// bool or (for ArgScalar) a Scalar.
	Args map[string]any
	// Column is the column the rule is for and Columns all the columns of its
	// table in row order.
	Column  Column
	Columns []Column
	// Key is DeriveKey of the salt, the name and the version of the transform; nil
	// for a transform that does not need a salt.
	Key []byte
}

// Input is what a Func is given for one row. Row holds every column of the row
// as it was read (before any rule of the table changed it), Index the position
// of the column the Func is for, and Value is Row[Index].
type Input struct {
	Value sqlvalue.Value
	Row   []sqlvalue.Value
	Index int
}

// Func computes the new value of one column of one row. It must be a pure
// function of its input, and safe to call from several goroutines at once (the
// tables of a dump are read in parallel). Neither the Func nor its errors may
// carry a value of the row.
type Func func(in Input) (sqlvalue.Value, error)

// keyLabel separates the keys of this tool's HMACs from any other use of the salt.
const keyLabel = "mysqldance/mask/v1"

// DeriveKey derives the HMAC key of a transform from the salt (HMAC-SHA256). The
// name and the version of the transform are part of the input, so a transform
// never shares a key with another, and a new version of a transform (one that
// gives other values) gets a new key: updating the tool alone does not change
// what a transform outputs.
func DeriveKey(salt Salt, name string, version int) []byte {
	m := hmac.New(sha256.New, salt.Bytes())
	m.Write([]byte(keyLabel))
	m.Write([]byte{0})
	m.Write([]byte(name))
	m.Write([]byte{0})
	m.Write([]byte(strconv.Itoa(version)))
	return m.Sum(nil)
}
