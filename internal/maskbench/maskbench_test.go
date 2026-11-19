// Package maskbench measures the cost of the per-cell transformations that
// data masking will need (B-201). It holds only benchmarks: they run with
// `go test -bench`; `task test` compiles them but does not run them. It does
// not touch product code.
//
//	go test ./internal/maskbench -run '^$' -bench . -benchmem
package maskbench

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash"
	"runtime"
	"strings"
	"testing"
	"unicode/utf8"
)

var key = []byte("maskbench-fixed-key-0123456789ab")

// shaper derives a value of the same byte length and character class from
// HMAC-SHA256 output. One shaper is owned by one goroutine (the hash state is
// reused, as a real implementation would do).
type shaper struct {
	mac  hash.Hash
	sum  []byte
	ctr  [4]byte
	out  []byte
	pool []byte
}

func newShaper() *shaper {
	return &shaper{mac: hmac.New(sha256.New, key), sum: make([]byte, 0, sha256.Size)}
}

// stream fills s.pool with at least n pseudo-random bytes derived from v.
func (s *shaper) stream(v []byte, n int) []byte {
	s.pool = s.pool[:0]
	for c := uint32(0); len(s.pool) < n; c++ {
		s.mac.Reset()
		s.mac.Write(v)
		binary.BigEndian.PutUint32(s.ctr[:], c)
		s.mac.Write(s.ctr[:])
		s.sum = s.mac.Sum(s.sum[:0])
		s.pool = append(s.pool, s.sum...)
	}
	return s.pool
}

// shape returns a value with the same length in bytes and the same class for
// every rune (digit, upper, lower, hiragana, emoji; anything else is kept).
func (s *shaper) shape(v []byte) []byte {
	ks := s.stream(v, utf8.RuneCount(v)) // one byte of key stream per rune
	s.out = s.out[:0]
	i := 0
	for p := 0; p < len(v); {
		r, w := utf8.DecodeRune(v[p:])
		p += w
		k := int(ks[i])
		i++
		if r == utf8.RuneError && w == 1 {
			// Invalid UTF-8: keep the byte as it is, so the length never changes.
			s.out = append(s.out, v[p-1])
			continue
		}
		switch {
		case r >= '0' && r <= '9':
			r = '0' + rune(k%10)
		case r >= 'a' && r <= 'z':
			r = 'a' + rune(k%26)
		case r >= 'A' && r <= 'Z':
			r = 'A' + rune(k%26)
		case r >= 0x3041 && r <= 0x3093: // hiragana, 3 bytes
			r = 0x3041 + rune(k%83)
		case r >= 0x1F600 && r <= 0x1F64F: // emoticons, 4 bytes
			r = 0x1F600 + rune(k%80)
		}
		s.out = utf8.AppendRune(s.out, r)
	}
	return s.out
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
				if bad := s.shape([]byte("ab\xffcd")); len(bad) != 5 || bad[2] != 0xff {
					b.Fatalf("shape must keep invalid bytes and the length, got %q", bad)
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
