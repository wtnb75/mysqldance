package dump

import (
	"fmt"
	"math"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5"

	"mysqldance/internal/datafile"
)

// TestPostgresFloatDoubleRoundTrip checks that FLOAT / DOUBLE values come back
// from a PostgreSQL dump and restore with the same bits (B-092). The bits are
// compared through float4send / float8send, which tell -0 from 0.
func TestPostgresFloatDoubleRoundTrip(t *testing.T) {
	e := newEnv(t)
	pg := newPGEnv(t)
	db := uniqueName(t)
	e.createDB(t, db)
	e.exec(t, "CREATE TABLE fp (id INT PRIMARY KEY, f FLOAT, d DOUBLE)")

	floats := []float32{
		0.1, 1e38, math.MaxFloat32, 1.17549435e-38, 16777216, 16777215, 123456789,
		0.33333334, -0.1, -math.MaxFloat32,
		// Zero, negative zero and subnormals.
		0, float32(math.Copysign(0, -1)), math.SmallestNonzeroFloat32, 1e-40, -1e-40,
	}
	doubles := []float64{
		0.1, 1e308, math.MaxFloat64, 2.2250738585072014e-308, 9007199254740993,
		0.30000000000000004, 4.35, -0.1, -math.MaxFloat64, 1.0000000000000002,
		0, math.Copysign(0, -1), math.SmallestNonzeroFloat64, 1e-310, -1e-310,
	}
	// Parameters go over the binary protocol, so the server receives the exact bits.
	if e.mc.InterpolateParams {
		t.Fatal("the test DSN must not set interpolateParams")
	}
	for i := range floats {
		if _, err := e.admin.ExecContext(t.Context(),
			"INSERT INTO fp (id, f, d) VALUES (?, ?, ?)", i+1, floats[i], doubles[i]); err != nil {
			t.Fatalf("insert row %d (%v, %v): %v", i+1, floats[i], doubles[i], err)
		}
	}
	before := e.floatBits(t)

	dir := t.TempDir()
	e.dumpPostgres(t, dir, datafile.Options{}, db)
	pg.restore(t, dir)
	conn := pg.connect(t)
	got := pgCells(t, conn, db,
		"SELECT id::text, encode(float4send(f), 'hex'), encode(float8send(d), 'hex') FROM fp ORDER BY fp.id")
	if len(got) != len(before) {
		t.Fatalf("restored %d rows, want %d", len(got), len(before))
	}
	bad := 0
	for i, r := range before {
		want := fmt.Sprintf("\"%d\",\"%08x\",\"%016x\"", r.id, r.f, r.d)
		if got[i] == want {
			continue
		}
		bad++
		t.Errorf("row %d (%v, %v): got %s, want %s", r.id,
			math.Float32frombits(r.f), math.Float64frombits(r.d), got[i], want)
	}
	if bad > 0 {
		t.Errorf("%d of %d rows changed", bad, len(before))
	}
}

// A column default of -0 goes through the same literal as a row value, so a row
// that takes the default after the restore holds -0 too (B-092).
func TestPostgresFloatNegativeZeroDefault(t *testing.T) {
	e := newEnv(t)
	pg := newPGEnv(t)
	db := uniqueName(t)
	e.createDB(t, db)
	// MySQL may keep DEFAULT -0 as the integer 0, so the expectation is what
	// MySQL itself stores for a row that takes the default, not a fixed -0.
	e.exec(t, "CREATE TABLE fd (id INT PRIMARY KEY, f FLOAT NOT NULL DEFAULT -0, d DOUBLE NOT NULL DEFAULT -0,"+
		" f2 FLOAT NOT NULL DEFAULT '-0', d2 DOUBLE NOT NULL DEFAULT '-0')",
		"INSERT INTO fd (id) VALUES (1)")
	var f, d, f2, d2 float64
	if err := e.admin.QueryRowContext(t.Context(),
		"SELECT f, d, f2, d2 FROM fd WHERE 1 = ?", 1).Scan(&f, &d, &f2, &d2); err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf(`"%08x","%016x","%08x","%016x"`,
		math.Float32bits(float32(f)), math.Float64bits(d), math.Float32bits(float32(f2)), math.Float64bits(d2))
	t.Logf("MySQL stores the defaults as %s", want)

	dir := t.TempDir()
	e.dumpPostgres(t, dir, datafile.Options{}, db)
	pg.restore(t, dir)
	conn := pg.connect(t)
	if _, err := conn.Exec(t.Context(), "INSERT INTO "+pgx.Identifier{db, "fd"}.Sanitize()+" (id) VALUES (2)"); err != nil {
		t.Fatal(err)
	}
	got := pgCells(t, conn, db, "SELECT encode(float4send(f), 'hex'), encode(float8send(d), 'hex'),"+
		" encode(float4send(f2), 'hex'), encode(float8send(d2), 'hex') FROM fd ORDER BY fd.id")
	// Row 1 was dumped (MySQL's own bits); row 2 took the restored defaults.
	if want := []string{want, want}; !reflect.DeepEqual(got, want) {
		t.Errorf("a row that takes the default = %v, want %v", got, want)
	}
}
