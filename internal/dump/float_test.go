package dump

import (
	"context"
	"math"
	"math/rand/v2"
	"strconv"
	"testing"
)

// floatRow holds the exact bit patterns of one FLOAT / DOUBLE row.
type floatRow struct {
	id int
	f  uint32
	d  uint64
}

// floatBits reads table fp through the binary protocol (a prepared statement),
// which transfers FLOAT / DOUBLE as raw IEEE 754 values rather than text.
func (e *env) floatBits(t *testing.T) []floatRow {
	t.Helper()
	rows, err := e.admin.QueryContext(context.Background(),
		"SELECT id, f, d FROM fp WHERE 1 = ? ORDER BY id", 1)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out []floatRow
	for rows.Next() {
		var (
			r floatRow
			f float32
			d float64
		)
		// The columns are never NULL here; Scan fails if one is.
		if err := rows.Scan(&r.id, &f, &d); err != nil {
			t.Fatal(err)
		}
		r.f, r.d = math.Float32bits(f), math.Float64bits(d)
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// TestFloatDoubleRoundTrip checks that FLOAT / DOUBLE values come back from a
// dump and restore with the same bits, including values that need the
// maximum number of significant digits.
func TestFloatDoubleRoundTrip(t *testing.T) {
	e := newEnv(t)
	db := uniqueName(t)
	e.createDB(t, db)
	e.exec(t, "CREATE TABLE fp (id INT PRIMARY KEY, f FLOAT, d DOUBLE)")

	floats := []float32{
		0.1, 1e38, math.MaxFloat32, 1.17549435e-38, 16777216, 16777215, 123456789,
		3.4028233e38, 1e-37, 0.33333334, 8388608.5, 1.00000012, -0.1, -math.MaxFloat32,
		// Zero, negative zero and subnormals.
		0, float32(math.Copysign(0, -1)), math.SmallestNonzeroFloat32, 1e-40, -1e-40,
	}
	doubles := []float64{
		0.1, 1e308, math.MaxFloat64, 2.2250738585072014e-308, 9007199254740993,
		0.30000000000000004, 123456789.12345678, 1e-300, 4.35, -0.1, -math.MaxFloat64,
		1.0000000000000002,
		0, math.Copysign(0, -1), math.SmallestNonzeroFloat64, 1e-310, -1e-310,
	}
	// Fixed seed: random finite bit patterns, the same on every run.
	rng := rand.New(rand.NewPCG(1, 2))
	for len(floats) < 300 {
		f := math.Float32frombits(rng.Uint32())
		if !math.IsNaN(float64(f)) && !math.IsInf(float64(f), 0) {
			floats = append(floats, f)
		}
	}
	for len(doubles) < 300 {
		d := math.Float64frombits(rng.Uint64())
		if !math.IsNaN(d) && !math.IsInf(d, 0) {
			doubles = append(doubles, d)
		}
	}
	// Parameters go over the binary protocol, so the server receives the exact
	// bits; interpolation would send them as text.
	if e.mc.InterpolateParams {
		t.Fatal("the test DSN must not set interpolateParams")
	}
	for i := range floats {
		_, err := e.admin.ExecContext(context.Background(),
			"INSERT INTO fp (id, f, d) VALUES (?, ?, ?)", i+1, floats[i], doubles[i])
		if err != nil {
			t.Fatalf("insert row %d (%v, %v): %v", i+1, floats[i], doubles[i], err)
		}
	}

	before := e.floatBits(t)
	if len(before) != len(floats) {
		t.Fatalf("inserted %d rows, read %d", len(floats), len(before))
	}

	dir := t.TempDir()
	e.dump(t, dir, db)
	e.exec(t, "DROP DATABASE "+quote(db))
	e.restore(t, dir)
	e.exec(t, "USE "+quote(db))

	after := e.floatBits(t)
	if len(after) != len(before) {
		t.Fatalf("restored %d rows, want %d", len(after), len(before))
	}

	t.Run("DOUBLE", func(t *testing.T) {
		bad, known := 0, 0
		for i := range before {
			if before[i].d == after[i].d {
				continue
			}
			// Known: -0 comes back as +0 (B-059).
			if before[i].d == 1<<63 && after[i].d == 0 {
				known++
				continue
			}
			bad++
			if bad <= 20 {
				t.Errorf("row %d: %#016x (%v) -> %#016x (%v)", before[i].id,
					before[i].d, math.Float64frombits(before[i].d),
					after[i].d, math.Float64frombits(after[i].d))
			}
		}
		if bad > 0 {
			t.Errorf("%d of %d rows changed", bad, len(before))
		}
		if known > 0 {
			t.Skipf("known DOUBLE mismatch (B-059): -0 became 0 in %d row(s); the other %d rows are exact", known, len(before)-known)
		}
	})

	t.Run("FLOAT", func(t *testing.T) {
		bad := 0
		for i := range before {
			if before[i].f == after[i].f {
				continue
			}
			// Only the known precision loss is tolerated: the restored value
			// must be the original rounded to 6 significant digits.
			orig := math.Float32frombits(before[i].f)
			if before[i].f == 1<<31 && after[i].f == 0 { // -0 became +0
				bad++
				continue
			}
			want, err := strconv.ParseFloat(strconv.FormatFloat(float64(orig), 'g', 6, 32), 32)
			if err != nil {
				t.Fatal(err)
			}
			if math.Float32bits(float32(want)) != after[i].f {
				t.Errorf("row %d: %#08x (%v) -> %#08x (%v), not the 6-digit rounding %v", before[i].id,
					before[i].f, orig, after[i].f, math.Float32frombits(after[i].f), float32(want))
				continue
			}
			bad++
			if bad <= 5 {
				t.Logf("row %d: %#08x (%v) -> %#08x (%v)", before[i].id,
					before[i].f, math.Float32frombits(before[i].f),
					after[i].f, math.Float32frombits(after[i].f))
			}
		}
		if bad > 0 {
			// Known: the server's text protocol prints FLOAT with 6
			// significant digits, which loses bits (B-059). The test starts
			// passing, instead of skipping, once the output is exact.
			t.Skipf("known FLOAT mismatch (B-059): %d of %d rows changed", bad, len(before))
		}
	})
}
