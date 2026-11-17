package dump

import (
	"math"
	"strings"
	"testing"

	"mysqldance/internal/datafile"
)

// TestSQLiteFloatDoubleRoundTrip checks that FLOAT / DOUBLE values come back
// from a SQLite dump and restore with the same bits (B-093). SQLite keeps every
// value as an 8-byte double, so a FLOAT is compared after narrowing it back to
// 32 bits.
//
// The one value that cannot come back is -0: a REAL-affinity column stores a
// whole-number double as an integer, which drops the sign, whatever the
// literal is (checked with the sqlite3 command: atan2(0.0, a) is 0 for a
// stored -0.0, and pi for the bare -0.0 expression). The test pins that, and that the dump warns of it, so a
// change in either direction is noticed.
func TestSQLiteFloatDoubleRoundTrip(t *testing.T) {
	e := newEnv(t)
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
	log := e.dumpSQLite(t, dir, datafile.Options{}, db)
	// The -0 that SQLite cannot keep is reported, once per column, without a value.
	for _, col := range []string{"f", "d"} {
		want := "column `" + col + "` holds 1 value of -0, which SQLite stores as 0"
		if !strings.Contains(log, want) {
			t.Errorf("the log does not say %q:\n%s", want, log)
		}
	}
	lite := restoreSQLite(t, dir)
	rows, err := lite.QueryContext(t.Context(), "SELECT id, f, d FROM fp ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	i, bad := 0, 0
	for rows.Next() {
		var (
			id int
			f  float64
			d  float64
		)
		if err := rows.Scan(&id, &f, &d); err != nil {
			t.Fatal(err)
		}
		if i >= len(before) {
			t.Fatalf("restored more than %d rows", len(before))
		}
		r := before[i]
		i++
		wantF, wantD := r.f, r.d
		// SQLite cannot store -0 in a REAL column; it comes back as +0.
		if wantF == math.Float32bits(float32(math.Copysign(0, -1))) {
			wantF = 0
		}
		if wantD == math.Float64bits(math.Copysign(0, -1)) {
			wantD = 0
		}
		gotF, gotD := math.Float32bits(float32(f)), math.Float64bits(d)
		if id == r.id && gotF == wantF && gotD == wantD {
			continue
		}
		bad++
		t.Errorf("row %d (id %d): got %08x,%016x, want %08x,%016x", r.id, id, gotF, gotD, wantF, wantD)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if i != len(before) {
		t.Fatalf("restored %d rows, want %d", i, len(before))
	}
	if bad > 0 {
		t.Errorf("%d of %d rows changed", bad, len(before))
	}
}
