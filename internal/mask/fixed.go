package mask

import (
	"fmt"
	"math"
	"math/big"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"mysqldance/internal/sqlvalue"
)

// DefaultRegistry returns the registry of the transforms this build has.
func DefaultRegistry() *Registry {
	reg := NewRegistry()
	for _, t := range []Transform{fixedTransform, nullifyTransform, keepTransform, hashKeepShapeTransform, hashEmailTransform} {
		if err := reg.Register(t); err != nil {
			panic(err) // a mistake in this file, caught by the tests
		}
	}
	return reg
}

// fixedTypes are the column types a fixed value can be written to.
var fixedTypes = []string{
	"CHAR", "VARCHAR", "TEXT",
	"TINYINT", "SMALLINT", "MEDIUMINT", "INT", "BIGINT",
	"DECIMAL", "FLOAT", "DOUBLE",
	"DATE", "DATETIME", "TIMESTAMP", "TIME", "YEAR",
}

// fixedTransform replaces every value that is not NULL with one value from the
// rule file. NULL stays NULL.
var fixedTransform = Transform{
	Name:       "fixed",
	Args:       []ArgDef{{Name: "value", Kind: ArgScalar, Required: true}},
	Types:      fixedTypes,
	Version:    1,
	BreaksKeys: true,
	Build:      buildFixed,
	BuildLeaf:  buildFixedLeaf,
}

// nullifyTransform replaces every value with NULL; it applies to every type.
var nullifyTransform = Transform{
	Name:      "nullify",
	Version:   1,
	Build:     buildNullify,
	BuildLeaf: buildNullifyLeaf,
}

// keepTransform writes the value as it is. Without --strict-columns it does the
// same as no rule; it is the record that someone decided the column needs no
// masking, which --strict-columns asks for.
var keepTransform = Transform{
	Name:     "keep",
	Version:  1,
	NoChange: true,
}

func buildNullify(c BuildContext) (Func, error) {
	if !c.Column.Nullable {
		return nil, fmt.Errorf("column %q is NOT NULL, so it cannot be set to NULL", c.Column.Name)
	}
	return func(in Input) (sqlvalue.Value, error) {
		return sqlvalue.NewValue(in.Value.Type(), nil, true), nil
	}, nil
}

// buildNullifyLeaf replaces the value a JSON path reaches with null, whatever it
// is (a container too).
func buildNullifyLeaf(BuildContext) (LeafFunc, error) {
	return func(Leaf) (Leaf, error) { return Leaf{Kind: LeafNull}, nil }, nil
}

// buildFixedLeaf writes the value of the rule in place of a JSON string, number
// or boolean of the same type. Which type the leaf has is known only when a row
// is read, so a leaf of another type is an error then; the number is checked
// now, because a number that JSON cannot write is wrong for every row.
func buildFixedLeaf(c BuildContext) (LeafFunc, error) {
	sc, ok := c.Args["value"].(Scalar)
	if !ok {
		return nil, fmt.Errorf("transform %q needs argument %q", "fixed", "value")
	}
	want := LeafString
	switch sc.Kind {
	case ScalarInt, ScalarFloat:
		want = LeafNumber
		// A rule file's number has at most maxScalarText characters, so it fits a
		// double and only the grammar needs checking.
		if end, ok := scanNumber([]byte(sc.Text), 0); !ok || end != len(sc.Text) {
			return nil, fmt.Errorf("the value of fixed cannot be written as a JSON number (a number has digits before and after a decimal point, and no leading +)")
		}
	case ScalarBool:
		want = LeafBool
	}
	text := []byte(sc.Text)
	return func(in Leaf) (Leaf, error) {
		if in.Kind != want {
			return Leaf{}, fmt.Errorf("the value of fixed does not fit: it is %s, but the JSON value is %s", want, in.Kind)
		}
		return Leaf{Kind: want, Text: text}, nil
	}, nil
}

func buildFixed(c BuildContext) (Func, error) {
	sc, ok := c.Args["value"].(Scalar)
	if !ok {
		return nil, fmt.Errorf("transform %q needs argument %q", "fixed", "value")
	}
	text, err := fixedText(sc, c.Column)
	if err != nil {
		return nil, err
	}
	raw := []byte(text)
	return func(in Input) (sqlvalue.Value, error) {
		if in.Value.IsNull() {
			return in.Value, nil
		}
		return sqlvalue.NewValue(in.Value.Type(), raw, false), nil
	}, nil
}

// fixedText checks that the value fits the column and returns it as the server
// would give it (the text a dump carries for the column). No message carries the
// value.
func fixedText(sc Scalar, col Column) (string, error) {
	bad := func(format string, args ...any) (string, error) {
		return "", fmt.Errorf("the value of fixed does not fit column %q (%s): %s",
			col.Name, col.ColumnType, fmt.Sprintf(format, args...))
	}
	needs := func(kinds ...ScalarKind) (string, bool) {
		if slices.Contains(kinds, sc.Kind) {
			return "", true
		}
		return fmt.Sprintf("the column takes %s values, but the value is of the type %s", kindList(kinds), sc.Kind), false
	}
	ct := strings.ToLower(col.ColumnType)
	params := typeParams(ct)
	unsigned := strings.Contains(ct, "unsigned")
	switch dt := strings.ToLower(col.DataType); dt {
	case "char", "varchar", "tinytext", "text", "mediumtext", "longtext":
		if msg, ok := needs(ScalarString); !ok {
			return bad("%s", msg)
		}
		if err := checkString(sc.Text, dt, params, col.CharacterSet); err != nil {
			return bad("%v", err)
		}
		return sc.Text, nil
	case "tinyint", "smallint", "mediumint", "int", "bigint":
		if sc.Kind == ScalarBool {
			// A boolean is the usual way to write a TINYINT(1) flag.
			if dt != "tinyint" || len(params) != 1 || params[0] != 1 {
				return bad("a boolean can be used only for a TINYINT(1) column")
			}
			if sc.Text == "true" {
				return "1", nil
			}
			return "0", nil
		}
		if msg, ok := needs(ScalarInt); !ok {
			return bad("%s", msg)
		}
		if err := checkInt(sc.Text, dt, unsigned); err != nil {
			return bad("%v", err)
		}
		return sc.Text, nil
	case "decimal":
		if msg, ok := needs(ScalarInt, ScalarFloat); !ok {
			return bad("%s", msg)
		}
		out, err := checkDecimal(sc.Text, params, unsigned)
		if err != nil {
			return bad("%v", err)
		}
		return out, nil
	case "float", "double":
		if msg, ok := needs(ScalarInt, ScalarFloat); !ok {
			return bad("%s", msg)
		}
		bits := 64
		if dt == "float" {
			bits = 32
		}
		f, err := strconv.ParseFloat(sc.Text, bits)
		if err != nil || math.IsInf(f, 0) || math.IsNaN(f) {
			return bad("the number is out of the range of the type")
		}
		return strconv.FormatFloat(f, 'g', -1, bits), nil
	case "date", "datetime", "timestamp", "time", "year":
		if msg, ok := needs(ScalarString); !ok {
			return bad("%s", msg)
		}
		out, err := checkTemporal(sc.Text, dt, params)
		if err != nil {
			return bad("%v", err)
		}
		return out, nil
	default:
		return bad("the type %s is not supported", col.TypeName)
	}
}

func kindList(kinds []ScalarKind) string {
	names := make([]string, len(kinds))
	for i, k := range kinds {
		names[i] = k.String()
	}
	return strings.Join(names, " or ")
}

// typeParams returns the numbers in the first parentheses of a COLUMN_TYPE:
// varchar(20) gives [20], decimal(10,2) gives [10 2].
func typeParams(columnType string) []int {
	open := strings.IndexByte(columnType, '(')
	closing := strings.IndexByte(columnType, ')')
	if open < 0 || closing < open {
		return nil
	}
	var out []int
	for f := range strings.SplitSeq(columnType[open+1:closing], ",") {
		n, err := strconv.Atoi(strings.TrimSpace(f))
		if err != nil {
			return nil // enum('a','b') and the like
		}
		out = append(out, n)
	}
	return out
}

// textBytes are the byte limits of the TEXT types.
var textBytes = map[string]int{"tinytext": 255, "text": 65535, "mediumtext": 16777215}

func checkString(s, dt string, params []int, charset string) error {
	if !utf8.ValidString(s) {
		return fmt.Errorf("the value is not valid UTF-8")
	}
	switch strings.ToLower(charset) {
	case "utf8mb4":
	case "utf8mb3", "utf8":
		for _, r := range s {
			if r > 0xFFFF {
				return fmt.Errorf("the column's character set %s cannot hold a character outside the basic multilingual plane", charset)
			}
		}
	default:
		for i := 0; i < len(s); i++ {
			if s[i] >= 0x80 {
				return fmt.Errorf("the column's character set %q is not known to hold characters other than ASCII", charset)
			}
		}
	}
	if dt == "char" || dt == "varchar" {
		if len(params) == 1 && utf8.RuneCountInString(s) > params[0] {
			return fmt.Errorf("the value is longer than %d characters", params[0])
		}
		return nil
	}
	if limit, ok := textBytes[dt]; ok && len(s) > limit {
		return fmt.Errorf("the value is longer than %d bytes", limit)
	}
	return nil
}

// intBits are the widths of the integer types.
var intBits = map[string]uint{"tinyint": 8, "smallint": 16, "mediumint": 24, "int": 32, "bigint": 64}

func checkInt(text, dt string, unsigned bool) error {
	n, ok := new(big.Int).SetString(text, 10)
	if !ok {
		return fmt.Errorf("not an integer")
	}
	bits := intBits[dt]
	var lo, hi *big.Int
	if unsigned {
		lo, hi = big.NewInt(0), new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), bits), big.NewInt(1))
	} else {
		half := new(big.Int).Lsh(big.NewInt(1), bits-1)
		lo, hi = new(big.Int).Neg(half), new(big.Int).Sub(half, big.NewInt(1))
	}
	if n.Cmp(lo) < 0 || n.Cmp(hi) > 0 {
		return fmt.Errorf("the number is out of the range of the type")
	}
	return nil
}

// checkDecimal checks the number against DECIMAL(M,D) and returns it with
// exactly D decimals, as the server prints it.
func checkDecimal(text string, params []int, unsigned bool) (string, error) {
	m, d := 10, 0 // what DECIMAL without (M,D) means
	switch len(params) {
	case 1:
		m = params[0]
	case 2:
		m, d = params[0], params[1]
	}
	r, ok := new(big.Rat).SetString(text)
	if !ok {
		return "", fmt.Errorf("not a number")
	}
	if unsigned && r.Sign() < 0 {
		return "", fmt.Errorf("the column is unsigned, but the number is negative")
	}
	scaled := new(big.Rat).Mul(r, new(big.Rat).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(d)), nil)))
	if !scaled.IsInt() {
		return "", fmt.Errorf("the number has more than %d decimals", d)
	}
	digits := new(big.Int).Abs(scaled.Num()).String()
	if scaled.Sign() != 0 && len(digits) > m {
		return "", fmt.Errorf("the number has more than %d digits", m)
	}
	return r.FloatString(d), nil
}

var (
	reDate     = regexp.MustCompile(`^(\d{4})-(\d{2})-(\d{2})$`)
	reDateTime = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2}) (\d{2}:\d{2}:\d{2})(?:\.(\d{1,6}))?$`)
	reTime     = regexp.MustCompile(`^(-)?(\d{1,3}):(\d{2}):(\d{2})(?:\.(\d{1,6}))?$`)
	reYear     = regexp.MustCompile(`^\d{4}$`)
)

// checkTemporal checks a date, time or year given as a string and returns it as
// the server prints it (the fraction padded to the precision of the column).
func checkTemporal(s, dt string, params []int) (string, error) {
	fsp := 0
	if len(params) == 1 {
		fsp = params[0]
	}
	// frac pads the fraction to fsp digits; more digits than fsp is an error.
	frac := func(f string) (string, error) {
		if len(f) > fsp {
			return "", fmt.Errorf("the fraction has more than %d digits", fsp)
		}
		if fsp == 0 {
			return "", nil
		}
		return "." + f + strings.Repeat("0", fsp-len(f)), nil
	}
	switch dt {
	case "date":
		if !reDate.MatchString(s) {
			return "", fmt.Errorf("a date is written YYYY-MM-DD")
		}
		t, err := time.Parse("2006-01-02", s)
		if err != nil || t.Year() < 1000 {
			return "", fmt.Errorf("not a date between 1000-01-01 and 9999-12-31")
		}
		return s, nil
	case "datetime", "timestamp":
		m := reDateTime.FindStringSubmatch(s)
		if m == nil {
			return "", fmt.Errorf("a date and time is written YYYY-MM-DD HH:MM:SS[.ffffff]")
		}
		t, err := time.Parse("2006-01-02 15:04:05", m[1]+" "+m[2])
		if err != nil || t.Year() < 1000 {
			return "", fmt.Errorf("not a date and time between 1000-01-01 and 9999-12-31")
		}
		if dt == "timestamp" && (t.Unix() < 1 || t.Unix() > math.MaxInt32) {
			return "", fmt.Errorf("a TIMESTAMP is between 1970-01-01 00:00:01 and 2038-01-19 03:14:07 UTC")
		}
		f, err := frac(m[3])
		if err != nil {
			return "", err
		}
		// 03:14:07 is the last second; a fraction after it is past the maximum.
		if dt == "timestamp" && t.Unix() == math.MaxInt32 && strings.Trim(m[3], "0") != "" {
			return "", fmt.Errorf("a TIMESTAMP is between 1970-01-01 00:00:01 and 2038-01-19 03:14:07 UTC")
		}
		return m[1] + " " + m[2] + f, nil
	case "time":
		m := reTime.FindStringSubmatch(s)
		if m == nil {
			return "", fmt.Errorf("a time is written [-]HH:MM:SS[.ffffff]")
		}
		h, _ := strconv.Atoi(m[2])
		mi, _ := strconv.Atoi(m[3])
		se, _ := strconv.Atoi(m[4])
		if h > 838 || mi > 59 || se > 59 {
			return "", fmt.Errorf("a TIME is between -838:59:59 and 838:59:59")
		}
		f, err := frac(m[5])
		if err != nil {
			return "", err
		}
		if h == 838 && mi == 59 && se == 59 && strings.Trim(m[5], "0") != "" {
			return "", fmt.Errorf("a TIME is between -838:59:59 and 838:59:59")
		}
		sign := m[1]
		if h == 0 && mi == 0 && se == 0 && strings.Trim(m[5], "0") == "" {
			sign = ""
		}
		return fmt.Sprintf("%s%02d:%02d:%02d%s", sign, h, mi, se, f), nil
	default: // year
		if !reYear.MatchString(s) {
			return "", fmt.Errorf("a year is written with four digits")
		}
		if y, _ := strconv.Atoi(s); y != 0 && (y < 1901 || y > 2155) {
			return "", fmt.Errorf("a YEAR is 0000 or between 1901 and 2155")
		}
		return s, nil
	}
}
