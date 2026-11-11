package sqlvalue

import (
	"math"
	"strconv"
	"testing"
)

func TestIsFloat(t *testing.T) {
	for typeName, want := range map[string]bool{
		"FLOAT": true, "float": true, "UNSIGNED FLOAT": true,
		"DOUBLE": false, "DECIMAL": false, "INT": false, "VARCHAR": false,
	} {
		if got := IsFloat(typeName); got != want {
			t.Errorf("IsFloat(%q) = %v, want %v", typeName, got, want)
		}
	}
}

func TestExactFloat(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		// A value the shortest float32 notation holds keeps that notation.
		{"0.1", "0.10000000149011612", "0.1"},
		{"one", "1", "1"},
		{"zero", "0", "0"},
		{"negative zero", "-0", "-0"},
		{"seven digits", "0.3333333432674408", "0.33333334"},
		{"large integer", "16777216", "1.6777216e+07"},
		{"negative", "-0.10000000149011612", "-0.1"},
		{"subnormal", "1.401298464324817e-45", "1e-45"},
		// 3.4028235e38 is above FLOAT's maximum as a double, and the server
		// refuses it as out of range; the exact value is used instead.
		{"max", "3.4028234663852886e38", "3.4028234663852886e+38"},
		{"negative max", "-3.4028234663852886e38", "-3.4028234663852886e+38"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ExactFloat([]byte(tt.in))
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tt.want {
				t.Errorf("ExactFloat(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestExactFloatErrors(t *testing.T) {
	for _, in := range []string{
		"",
		"abc",
		"NaN",
		"Inf",
		// Not a float32 value: a FLOAT column cannot have produced it.
		"0.1",
		// Out of float32 range.
		"1e39",
	} {
		if got, err := ExactFloat([]byte(in)); err == nil {
			t.Errorf("ExactFloat(%q) = %q, want an error", in, got)
		}
	}
}

// TestExactFloatRoundTrip checks that the output parses back to the same
// float32 for every kind of value, whether it is the shortest notation or the
// exact one.
func TestExactFloatRoundTrip(t *testing.T) {
	bits := []uint32{
		0x00000001, 0x007fffff, 0x00800000, 0x3dcccccd, 0x3eaaaaab, 0x4b800000,
		0x4b7fffff, 0x7f7fffff, 0x7f7ffffe, 0x80000000, 0x80000001, 0xff7fffff,
	}
	for i := range uint32(5000) {
		bits = append(bits, i*0x000d1c7b+0x1234)
	}
	for _, b := range bits {
		f := math.Float32frombits(b)
		if math.IsNaN(float64(f)) || math.IsInf(float64(f), 0) {
			continue
		}
		in := strconv.FormatFloat(float64(f), 'g', -1, 64)
		out, err := ExactFloat([]byte(in))
		if err != nil {
			t.Fatalf("%#08x (%s): %v", b, in, err)
		}
		d, err := strconv.ParseFloat(string(out), 64)
		if err != nil {
			t.Fatalf("%#08x: %q does not parse: %v", b, out, err)
		}
		if math.Abs(d) > math.MaxFloat32 {
			t.Errorf("%#08x: %q is above FLOAT's range", b, out)
		}
		if got := math.Float32bits(float32(d)); got != b {
			t.Errorf("%#08x: %q comes back as %#08x", b, out, got)
		}
	}
}
