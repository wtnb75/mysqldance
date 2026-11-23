// Package maskbench measures the cost of the per-cell transformations of data
// masking (B-201). The string transform is the real hash_keep_shape (B-167);
// the JSON rewrite is still a stand-in (B-169). It holds only benchmarks: they
// run with `go test -bench`; `task test` compiles them but does not run them.
// They call the product's mask and sqlvalue packages and change nothing in them.
//
//	go test ./internal/maskbench -run '^$' -bench . -benchmem
package maskbench

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"hash"
	"runtime"
	"strings"
	"testing"
	"unicode/utf8"

	"mysqldance/internal/mask"
	"mysqldance/internal/sqlvalue"
)

var key = []byte("maskbench-fixed-key-0123456789ab")

// shaper runs the real hash_keep_shape transform (internal/mask). One shaper
// is owned by one goroutine of a benchmark; the transform itself is safe to
// share, as in a dump. mac and sum are for BenchmarkHMACOnly, the floor.
type shaper struct {
	fn  mask.Func
	mac hash.Hash
	sum []byte
}

func newShaper() *shaper {
	tr, ok := mask.DefaultRegistry().Lookup("hash_keep_shape")
	if !ok {
		panic("hash_keep_shape is not registered")
	}
	fn, err := tr.Build(mask.BuildContext{
		Column: mask.Column{Name: "c", TypeName: "VARCHAR", CharacterSet: "utf8mb4"},
		Key:    key,
	})
	if err != nil {
		panic(err)
	}
	return &shaper{fn: fn, mac: hmac.New(sha256.New, key), sum: make([]byte, 0, sha256.Size)}
}

// shape returns the masked value: the same length in bytes and characters, and
// the same class for every character. It returns nil when the transform refuses
// the value (invalid UTF-8).
func (s *shaper) shape(v []byte) []byte {
	out, err := s.fn(mask.Input{Value: sqlvalue.NewValue("VARCHAR", v, false)})
	if err != nil {
		return nil
	}
	return out.Bytes()
}

// cell builds a deterministic cell of about n bytes of the given class.
func cell(class string, n int) []byte {
	var b []byte
	switch class {
	case "ascii":
		const alpha = "abcXYZ0123456789"
		for len(b) < n {
			b = append(b, alpha[len(b)%len(alpha)])
		}
	case "japanese":
		for len(b)+3 <= n {
			b = utf8.AppendRune(b, 0x3042+rune(len(b)/3%80))
		}
	case "emoji":
		for len(b)+4 <= n {
			b = utf8.AppendRune(b, 0x1F600+rune(len(b)/4%80))
		}
	}
	return b
}

var sizes = []int{8, 32, 256}
var classes = []string{"ascii", "japanese", "emoji"}

// BenchmarkHMACOnly is the floor: one HMAC-SHA256 of the cell, no shaping.
func BenchmarkHMACOnly(b *testing.B) {
	for _, n := range sizes {
		b.Run(fmt.Sprintf("ascii/%dB", n), func(b *testing.B) {
			v := cell("ascii", n)
			s := newShaper()
			b.SetBytes(int64(len(v)))
			b.ReportAllocs()
			for b.Loop() {
				s.mac.Reset()
				s.mac.Write(v)
				s.sum = s.mac.Sum(s.sum[:0])
			}
		})
	}
}

// BenchmarkShape is (a): HMAC plus a same-length, same-class output.
func BenchmarkShape(b *testing.B) {
	for _, c := range classes {
		for _, n := range sizes {
			b.Run(fmt.Sprintf("%s/%dB", c, n), func(b *testing.B) {
				v := cell(c, n)
				s := newShaper()
				got := s.shape(v)
				if len(got) != len(v) || bytes.Equal(got, v) {
					b.Fatalf("shape: len %d -> %d, same=%v", len(v), len(got), bytes.Equal(got, v))
				}
				if bad := s.shape([]byte("ab\xffcd")); bad != nil {
					b.Fatalf("invalid UTF-8 must be refused, got %q", bad)
				}
				b.SetBytes(int64(len(v)))
				b.ReportAllocs()
				for b.Loop() {
					s.shape(v)
				}
			})
		}
	}
}

// jsonDoc builds a JSON document of about n bytes with the value to rewrite
// at user.email. Numbers include ones a float64 cannot hold.
func jsonDoc(n int) []byte {
	var sb strings.Builder
	sb.WriteString(`{"user":{"email":"taro.yamada@example.com","name":"山田太郎"},"items":[`)
	for i := 0; ; i++ {
		item := fmt.Sprintf(`{"id":%d,"big":12345678901234567890.123456789,"price":%d.50,"note":"item-%d","tags":["a","b"]}`, i, i, i)
		if sb.Len()+len(item)+2 > n {
			break
		}
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString(item)
	}
	sb.WriteString(`]}`)
	return []byte(sb.String())
}

var jsonSizes = []struct {
	name string
	n    int
}{{"200B", 200}, {"2KB", 2 << 10}, {"20KB", 20 << 10}}

// parseJSON reads a document, keeping numbers as the text that was read.
func parseJSON(doc []byte) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(doc))
	dec.UseNumber()
	var root map[string]any
	err := dec.Decode(&root)
	return root, err
}

// rewriteJSON parses (numbers kept as the text that was read), rewrites
// user.email and writes the document back out.
func rewriteJSON(s *shaper, doc []byte) ([]byte, error) {
	root, err := parseJSON(doc)
	if err != nil {
		return nil, err
	}
	user, _ := root["user"].(map[string]any)
	if email, ok := user["email"].(string); ok {
		user["email"] = string(s.shape([]byte(email)))
	}
	return json.Marshal(root)
}

// BenchmarkJSON is (b): parse, rewrite one path, write back.
func BenchmarkJSON(b *testing.B) {
	for _, sz := range jsonSizes {
		doc := jsonDoc(sz.n)
		s := newShaper()
		out, err := rewriteJSON(s, doc)
		if err != nil {
			b.Fatal(err)
		}
		if !bytes.Contains(out, []byte("12345678901234567890.123456789")) {
			b.Fatal("number was not kept as read")
		}
		b.Run(sz.name+"/full", func(b *testing.B) {
			b.SetBytes(int64(len(doc)))
			b.ReportAllocs()
			for b.Loop() {
				if _, err := rewriteJSON(s, doc); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(sz.name+"/parse-only", func(b *testing.B) {
			b.SetBytes(int64(len(doc)))
			b.ReportAllocs()
			for b.Loop() {
				if _, err := parseJSON(doc); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkParallel is (c): the same work on 1, 2, 4 and 8 goroutines.
// ns/op is wall time per cell, so a perfect scale-out divides it by the count.
func BenchmarkParallel(b *testing.B) {
	doc := jsonDoc(2 << 10)
	v := cell("ascii", 32)
	cases := []struct {
		name string
		work func(s *shaper) error
	}{
		{"shape-ascii-32B", func(s *shaper) error { s.shape(v); return nil }},
		{"json-2KB", func(s *shaper) error { _, err := rewriteJSON(s, doc); return err }},
	}
	for _, c := range cases {
		for _, procs := range []int{1, 2, 4, 8} {
			b.Run(fmt.Sprintf("%s/procs=%d", c.name, procs), func(b *testing.B) {
				if procs > runtime.NumCPU() {
					b.Skipf("needs %d CPUs, have %d", procs, runtime.NumCPU())
				}
				prev := runtime.GOMAXPROCS(procs)
				defer runtime.GOMAXPROCS(prev)
				b.ReportAllocs()
				b.RunParallel(func(pb *testing.PB) {
					s := newShaper()
					for pb.Next() {
						if err := c.work(s); err != nil {
							b.Error(err)
							return
						}
					}
				})
			})
		}
	}
}
