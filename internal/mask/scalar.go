package mask

import (
	"errors"
	"fmt"
	"math/big"
	"strings"

	"go.yaml.in/yaml/v3"
)

// ScalarKind is the YAML type of a Scalar.
type ScalarKind int

// The YAML types a Scalar can have.
const (
	ScalarString ScalarKind = iota
	ScalarInt
	ScalarFloat
	ScalarBool
)

func (k ScalarKind) String() string {
	switch k {
	case ScalarString:
		return "string"
	case ScalarInt:
		return "integer"
	case ScalarFloat:
		return "decimal number"
	default:
		return "boolean"
	}
}

// Scalar is the value of an ArgScalar argument. It keeps the text, not a Go
// number: a float64 would drop the digits of a DECIMAL(30,10), and a value is
// checked against its column from the text. Text is the string as it is; for an
// integer the decimal digits (a leading "-" if negative, no "+", no base
// prefix); for a decimal number the text as written without "_"; for a boolean
// "true" or "false".
type Scalar struct {
	Kind ScalarKind
	Text string
}

// maxScalarText bounds a number written in a rule file, so that a huge exponent
// cannot make the checks allocate without limit.
const maxScalarText = 200

func newScalar(v *yaml.Node) (Scalar, error) {
	switch v.Tag {
	case "!!str", "!!timestamp":
		return Scalar{Kind: ScalarString, Text: v.Value}, nil
	case "!!bool":
		return Scalar{Kind: ScalarBool, Text: strings.ToLower(v.Value)}, nil
	}
	text := strings.ReplaceAll(v.Value, "_", "")
	if len(text) > maxScalarText {
		return Scalar{}, errors.New("the number is too long")
	}
	if v.Tag == "!!int" {
		n, ok := new(big.Int).SetString(text, 0)
		if !ok {
			return Scalar{}, fmt.Errorf("%q is not an integer", v.Value)
		}
		return Scalar{Kind: ScalarInt, Text: n.String()}, nil
	}
	// !!float: .inf and .nan are not numbers a column can hold.
	if _, ok := new(big.Rat).SetString(text); !ok {
		return Scalar{}, errors.New("the number must be finite")
	}
	return Scalar{Kind: ScalarFloat, Text: text}, nil
}
