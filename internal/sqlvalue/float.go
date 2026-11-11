package sqlvalue

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// IsFloat reports whether typeName (database/sql's DatabaseTypeName) is FLOAT.
// The server's text protocol prints a FLOAT with 6 significant digits, which
// loses bits, so the source reads it as a DOUBLE and passes it to ExactFloat.
func IsFloat(typeName string) bool {
	t := strings.ToUpper(strings.TrimSpace(typeName))
	return strings.TrimPrefix(t, "UNSIGNED ") == "FLOAT"
}

// isNegativeZero reports whether raw, the text of a FLOAT or DOUBLE, is a
// negative zero in any spelling ("-0", "-0.0", "-0e0").
func isNegativeZero(raw []byte) bool {
	if len(raw) == 0 || raw[0] != '-' {
		return false
	}
	d, err := strconv.ParseFloat(string(raw), 64)
	return err == nil && d == 0
}

// ExactFloat turns the text of a FLOAT read as a DOUBLE (`col * 1e0`, the exact
// value of the float32) into the shortest notation that parses back to the same
// float32, so 0.10000000149011612 becomes 0.1. A float32 maximum such as
// 3.4028235e38 is above FLOAT's range as a double, and the server refuses it
// as out of range, so then the exact double notation is kept. A value that is
// not a float32 is an error rather than a silent rounding.
func ExactFloat(raw []byte) ([]byte, error) {
	d, err := strconv.ParseFloat(string(raw), 64)
	if err != nil {
		return nil, fmt.Errorf("FLOAT value %q: %w", raw, err)
	}
	f := float32(d)
	if math.IsNaN(d) || math.IsInf(d, 0) || float64(f) != d {
		return nil, fmt.Errorf("FLOAT value %q is not a finite single-precision number", raw)
	}
	short := strconv.FormatFloat(d, 'g', -1, 32)
	if p, err := strconv.ParseFloat(short, 64); err == nil && math.Abs(p) <= math.MaxFloat32 {
		return []byte(short), nil
	}
	return []byte(strconv.FormatFloat(d, 'g', -1, 64)), nil
}
