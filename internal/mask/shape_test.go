package mask

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"mysqldance/internal/sqlvalue"
)

const shapeSalt = "0123456789abcdef-shape-salt"

func shapeSaltOf(t *testing.T, s string) *Salt {
	t.Helper()
	salt, err := LoadSalt("", env(map[string]string{SaltEnv: s}))
	if err != nil {
		t.Fatal(err)
	}
	return &salt
}

// shapeCatalog has a key column (id), string columns of each character set and
// columns of other types.
func shapeCatalog() Catalog {
	return Catalog{Databases: map[string][]CatalogTable{"shop": {{
		Name: "users",
		Columns: []CatalogColumn{
			col("id", "VARCHAR", "varchar(20)", "varchar", "utf8mb4", false),
			col("v", "VARCHAR", "varchar(2000)", "varchar", "utf8mb4", true),
			col("w", "VARCHAR", "varchar(2000)", "varchar", "utf8mb4", true),
			col("u3", "VARCHAR", "varchar(2000)", "varchar", "utf8mb3", true),
			col("l1", "VARCHAR", "varchar(2000)", "varchar", "latin1", true),
			col("t", "TEXT", "text", "text", "utf8mb4", true),
			col("n", "INT", "int", "int", "", true),
			col("b", "BLOB", "blob", "blob", "", true),
			col("code", "VARCHAR", "varchar(10)", "varchar", "utf8mb4", true),
		},
		OrderBy: []string{"id"},
		Indexes: []CatalogIndex{
			{Name: "PRIMARY", Unique: true, Columns: []string{"id"}},
			{Name: "uq_code", Unique: true, Columns: []string{"code"}},
		},
	}}}}
}

func shapeRules(columns ...string) string {
	var b strings.Builder
	b.WriteString("version: 1\nrules:\n")
	for _, c := range columns {
		b.WriteString("  shop.users." + c + ":\n    use: hash_keep_shape\n")
	}
	return b.String()
}

// shapeRunner returns a function that masks one value of the column.
func shapeRunner(t *testing.T, salt string, column string) func(string) (string, error) {
	t.Helper()
	plan, err := resolveRules(t, shapeCatalog(), shapeRules(column), shapeSaltOf(t, salt))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	var cols []sqlvalue.Column
	idx := -1
	for i, c := range shapeCatalog().Databases["shop"][0].Columns {
		cols = append(cols, sqlvalue.Column{Name: c.Name, TypeName: c.TypeName})
		if c.Name == column {
			idx = i
		}
	}
	fn, err := plan.Table("shop", "users", cols)
	if err != nil {
		t.Fatal(err)
	}
	return func(s string) (string, error) {
		row := make([]sqlvalue.Value, len(cols))
		for i, c := range cols {
			row[i] = sqlvalue.NewValue(c.TypeName, []byte("0"), false)
		}
		row[idx] = sqlvalue.NewValue(cols[idx].TypeName, []byte(s), false)
		if err := fn(row); err != nil {
			return "", err
		}
		return string(row[idx].Bytes()), nil
	}
}

func TestHashKeepShapeIsRegistered(t *testing.T) {
	tr, ok := DefaultRegistry().Lookup("hash_keep_shape")
	if !ok {
		t.Fatal("hash_keep_shape is not registered")
	}
	if tr.Version != 1 || !tr.NeedsSalt || !tr.BreaksKeys || !tr.UniqueChecked || len(tr.Args) != 0 {
		t.Errorf("transform = %+v", tr)
	}
}

func TestHashKeepShapeColumns(t *testing.T) {
	salt := shapeSaltOf(t, shapeSalt)
	// code is a column of a unique index: the dump checks for collisions (B-168).
	for _, c := range []string{"v", "u3", "l1", "t", "code"} {
		if _, err := resolveRules(t, shapeCatalog(), shapeRules(c), salt); err != nil {
			t.Errorf("%s: %v", c, err)
		}
	}
	cases := []struct{ column, want string }{
		{"n", "does not apply"},
		{"b", "does not apply"},
		{"id", "key"},
	}
	for _, c := range cases {
		_, err := resolveRules(t, shapeCatalog(), shapeRules(c.column), salt)
		if err == nil || !strings.Contains(strings.ToLower(err.Error()), c.want) {
			t.Errorf("%s: err = %v, want %q", c.column, err, c.want)
		}
	}
}

func TestHashKeepShapeNeedsSalt(t *testing.T) {
	_, err := resolveRules(t, shapeCatalog(), shapeRules("v"), nil)
	if err == nil || !strings.Contains(err.Error(), "needs a salt") {
		t.Fatalf("err = %v", err)
	}
}

func TestHashKeepShapeIsDeterministic(t *testing.T) {
	a := shapeRunner(t, shapeSalt, "v")
	b := shapeRunner(t, shapeSalt, "v")
	other := shapeRunner(t, shapeSalt+"x", "v")
	in := "alice@example.com"
	x, err := a(in)
	if err != nil {
		t.Fatal(err)
	}
	if y, _ := b(in); x != y {
		t.Errorf("two runs differ: %q %q", x, y)
	}
	if y, _ := a(in); x != y {
		t.Errorf("one run differs: %q %q", x, y)
	}
	if z, _ := other(in); x == z {
		t.Errorf("another salt gives the same value %q", x)
	}
	if x == in {
		t.Errorf("the value is unchanged: %q", x)
	}
	if y, _ := a("alice@example.org"); y == x {
		t.Errorf("two values map to one: %q", x)
	}
}

// The key is shared by every column, so one value is one value everywhere.
func TestHashKeepShapeKeyIsCommonToColumns(t *testing.T) {
	v := shapeRunner(t, shapeSalt, "v")
	w := shapeRunner(t, shapeSalt, "w")
	a, _ := v("same value 42")
	b, _ := w("same value 42")
	if a != b {
		t.Errorf("columns differ: %q %q", a, b)
	}
}

func TestHashKeepShapeNullAndEmpty(t *testing.T) {
	plan, err := resolveRules(t, shapeCatalog(), shapeRules("v"), shapeSaltOf(t, shapeSalt))
	if err != nil {
		t.Fatal(err)
	}
	cols := []sqlvalue.Column{{Name: "id", TypeName: "INT"}, {Name: "v", TypeName: "VARCHAR"}}
	fn, err := plan.Table("shop", "users", cols)
	if err != nil {
		t.Fatal(err)
	}
	row := []sqlvalue.Value{sqlvalue.NewValue("INT", []byte("1"), false), sqlvalue.NewValue("VARCHAR", nil, true)}
	if err := fn(row); err != nil {
		t.Fatal(err)
	}
	if !row[1].IsNull() {
		t.Error("NULL was replaced")
	}
	run := shapeRunner(t, shapeSalt, "v")
	if got, err := run(""); err != nil || got != "" {
		t.Errorf("empty = %q, %v", got, err)
	}
}

// shapeClass returns the class of a rune and the byte length of its UTF-8
// form; two runes of one class and one length are what the transform may swap.
func shapeClass(r rune) string {
	switch {
	case r >= '0' && r <= '9':
		return "digit"
	case r >= 'A' && r <= 'Z':
		return "upper"
	case r >= 'a' && r <= 'z':
		return "lower"
	case r < 0x80:
		return "ascii:" + string(r) // kept as it is
	case r >= 0x3041 && r <= 0x309F:
		return "hiragana"
	case r >= 0x30A1 && r <= 0x30FF:
		return "katakana"
	case r >= 0x4E00 && r <= 0x9FFF:
		return "kanji"
	}
	switch utf8.RuneLen(r) {
	case 2:
		return "two"
	case 3:
		return "three"
	default:
		return "four"
	}
}

// outputClass is the class an input class is written in. Other 2-byte letters
// become Latin Extended-A, other 3-byte characters kanji, 4-byte characters
// emoji.
func outputClass(r rune) string {
	switch c := shapeClass(r); c {
	case "two":
		return "latinx"
	case "three":
		return "kanji"
	case "four":
		return "emoji"
	default:
		return c
	}
}

func outputRuneClass(r rune) string {
	switch {
	case r >= 0x0100 && r <= 0x017F:
		return "latinx"
	case r >= 0x1F600 && r <= 0x1F64F:
		return "emoji"
	}
	return shapeClass(r)
}

func TestHashKeepShapeKeepsTheShape(t *testing.T) {
	run := shapeRunner(t, shapeSalt, "v")
	inputs := []string{
		"a",
		"Hello, World! 0123456789",
		"03-1234-5678",
		"user.name+tag@example.co.jp",
		"ひらがなとカタカナと漢字と全角ＡＢＣ１２３", "コーヒー・ゝヽ",
		"こんにちは\t世界\n😀😃😄 end",
		"é ü ñ ß Ω Ж ö",
		"ｱｲｳ ー ・ 〒 ㈱ 한글",
		strings.Repeat("The quick brown fox 日本語 😀 ", 40),
	}
	for _, in := range inputs {
		out, err := run(in)
		if err != nil {
			t.Fatalf("%q: %v", in, err)
		}
		if !utf8.ValidString(out) {
			t.Fatalf("%q: not valid UTF-8", in)
		}
		if len(out) != len(in) || utf8.RuneCountInString(out) != utf8.RuneCountInString(in) {
			t.Errorf("%q: %d bytes %d chars became %d bytes %d chars", in,
				len(in), utf8.RuneCountInString(in), len(out), utf8.RuneCountInString(out))
		}
		ir, or := []rune(in), []rune(out)
		for i := range ir {
			if outputClass(ir[i]) != outputRuneClass(or[i]) {
				t.Errorf("%q: char %d %q (%s) became %q (%s)", in, i, ir[i], outputClass(ir[i]), or[i], outputRuneClass(or[i]))
				break
			}
			if utf8.RuneLen(ir[i]) != utf8.RuneLen(or[i]) {
				t.Errorf("%q: char %d changed its byte length", in, i)
				break
			}
		}
	}
}

// A long value needs more key stream than one HMAC gives; the end of it is
// still masked, and the same prefix gives the same start.
func TestHashKeepShapeLongValue(t *testing.T) {
	run := shapeRunner(t, shapeSalt, "v")
	in := strings.Repeat("abcdefghij", 150)
	out, err := run(in)
	if err != nil {
		t.Fatal(err)
	}
	if out[len(out)-200:] == in[len(in)-200:] {
		t.Error("the end of a long value is not masked")
	}
	if out[len(out)-100:] == out[len(out)-200:len(out)-100] {
		t.Error("the key stream repeats")
	}
}

// The Func is shared by the readers of a dump, so goroutines must not disturb
// each other: the output of one value is the same however many run at once.
func TestHashKeepShapeIsSafeForConcurrentUse(t *testing.T) {
	run := shapeRunner(t, shapeSalt, "v")
	inputs := []string{"alice@example.com", "山田太郎", strings.Repeat("x😀y", 100), "03-1234-5678"}
	want := make([]string, len(inputs))
	for i, in := range inputs {
		want[i], _ = run(in)
	}
	done := make(chan error, 8)
	for range 8 {
		go func() {
			for n := range 200 {
				i := n % len(inputs)
				if got, _ := run(inputs[i]); got != want[i] {
					done <- fmt.Errorf("value %d differs under concurrent use", i)
					return
				}
			}
			done <- nil
		}()
	}
	for range 8 {
		if err := <-done; err != nil {
			t.Error(err)
		}
	}
}

func TestHashKeepShapeInvalidUTF8(t *testing.T) {
	run := shapeRunner(t, shapeSalt, "v")
	_, err := run("ab\xffcdSECRETVALUE")
	if err == nil {
		t.Fatal("an invalid value was accepted")
	}
	for _, s := range []string{"SECRETVALUE", "ab"} {
		if strings.Contains(err.Error(), s) {
			t.Errorf("the error shows the value: %v", err)
		}
	}
	if !strings.Contains(err.Error(), `"v"`) {
		t.Errorf("the error does not name the column: %v", err)
	}
}

func TestHashKeepShapeCharacterSet(t *testing.T) {
	l1 := shapeRunner(t, shapeSalt, "l1")
	if _, err := l1("plain ASCII 123"); err != nil {
		t.Errorf("ASCII in latin1: %v", err)
	}
	_, err := l1("caféSECRETVALUE")
	if err == nil {
		t.Fatal("a non-ASCII value in a latin1 column was accepted")
	}
	if strings.Contains(err.Error(), "SECRETVALUE") || !strings.Contains(err.Error(), "latin1") {
		t.Errorf("err = %v", err)
	}
	u3 := shapeRunner(t, shapeSalt, "u3")
	out, err := u3("日本語とカタカナ é")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range out {
		if utf8.RuneLen(r) > 3 {
			t.Errorf("a utf8mb3 column got %q", r)
		}
	}
}
