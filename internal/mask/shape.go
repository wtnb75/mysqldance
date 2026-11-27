package mask

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"hash"
	"strings"
	"sync"
	"unicode/utf8"

	"mysqldance/internal/sqlvalue"
)

// shapeTypes are the column types hash_keep_shape applies to: the character
// strings. BINARY and BLOB (B-189), JSON (B-169), ENUM and SET are not here.
var shapeTypes = []string{"CHAR", "VARCHAR", "TEXT"}

// hashKeepShapeTransform replaces every character of a value with another of
// the same class and the same UTF-8 length, chosen by HMAC(key, value), so that
// the same value gives the same output, the length in bytes and in characters
// stays, and so do the digits, letters, kana, kanji and emoji as classes.
// Punctuation, spaces and control characters of ASCII are kept. NULL stays NULL.
//
// BreaksKeys: two values can give one output, so it is refused on the ordering
// key and the primary key, and on the child side of a foreign key unless the
// parent column has the same transform, arguments and domain (B-170). It may be
// used on the
// columns of another unique index (UniqueChecked): the dump compares the rows
// after the transform and stops with an error at a collision, never extending
// the output to avoid one, because the length is kept.
var hashKeepShapeTransform = Transform{
	Name:          "hash_keep_shape",
	Types:         shapeTypes,
	Version:       1,
	NeedsSalt:     true,
	BreaksKeys:    true,
	UniqueChecked: true,
	Build:         buildHashKeepShape,
	BuildLeaf:     buildShapeLeaf,
}

// The ranges the characters of the non-ASCII classes are drawn from. Each holds
// only assigned characters of one UTF-8 length.
const (
	hiraganaLo, hiraganaHi = 0x3041, 0x3096
	katakanaLo, katakanaHi = 0x30A1, 0x30FA
	// The input classes reach the rest of the kana blocks (the iteration marks, the
	// prolonged sound mark, the middle dot), which are written as kana of their
	// class instead of becoming kanji and showing where they are.
	hiraganaInHi = 0x309F
	katakanaInHi = 0x30FF
	// The kanji are the CJK Unified Ideographs block (U+4E00 to U+9FFF); the output
	// is drawn from the part of it that has been assigned since the first Unicode
	// versions.
	kanjiLo, kanjiOutHi = 0x4E00, 0x9FA5
	latinXLo            = 0x0100 // Latin Extended-A
	latinXHi            = 0x017F
	emojiLo             = 0x1F600 // Emoticons
	emojiHi             = 0x1F64F
)

func buildHashKeepShape(c BuildContext) (Func, error) {
	if len(c.Key) == 0 {
		return nil, fmt.Errorf("transform %q needs a salt", "hash_keep_shape")
	}
	// Copy what the Func needs, so that it does not keep all of c (the columns and
	// arguments of the table) alive for the whole dump.
	charset := c.Column.CharacterSet
	lower := strings.ToLower(charset)
	wideOK := lower == "utf8mb4" || lower == "utf8mb3" || lower == "utf8"
	key := c.Key
	pool := newShapePool(key)
	name := c.Column.Name
	return func(in Input) (sqlvalue.Value, error) {
		if in.Value.IsNull() {
			return in.Value, nil
		}
		raw := in.Value.Bytes()
		if len(raw) == 0 {
			return in.Value, nil
		}
		st := pool.Get().(*shapeState)
		out, err := st.shape(raw, wideOK)
		pool.Put(st)
		if err != nil {
			if errors.Is(err, errShapeUTF8) {
				return sqlvalue.Value{}, fmt.Errorf("a value of column %q is not valid UTF-8, so its length cannot be kept", name)
			}
			if errors.Is(err, errShapeCharset) {
				return sqlvalue.Value{}, fmt.Errorf("a value of column %q has a character other than ASCII, but the character set %s of the column is not known to hold the characters that would replace it (only utf8mb4, utf8mb3 and utf8 are)", name, charset)
			}
			return sqlvalue.Value{}, fmt.Errorf("column %q: %w", name, err)
		}
		return sqlvalue.NewValue(in.Value.Type(), out, false), nil
	}, nil
}

// buildShapeLeaf applies hash_keep_shape to the JSON strings a path reaches. The
// same string gives the same output as in a column (the key is the one of the
// transform, not of the column or the path).
func buildShapeLeaf(c BuildContext) (LeafFunc, error) {
	fn, err := buildHashKeepShape(c)
	if err != nil {
		return nil, err
	}
	return func(in Leaf) (Leaf, error) {
		if in.Kind != LeafString {
			return Leaf{}, fmt.Errorf("hash_keep_shape can only be used on a JSON string, but the value is %s", in.Kind)
		}
		v, err := fn(Input{Value: sqlvalue.NewValue("VARCHAR", in.Text, false)})
		if err != nil {
			return Leaf{}, err
		}
		return Leaf{Kind: LeafString, Text: v.Bytes()}, nil
	}, nil
}

var (
	errShapeUTF8    = errors.New("not valid UTF-8")
	errShapeCharset = errors.New("character set")
)

// shapeState is an HMAC state and the buffers of one call, kept together so that
// nothing escapes to the heap per value.
type shapeState struct {
	h    hash.Hash
	seed [sha256.Size]byte // HMAC(key, value): block 0 of the key stream
	blk  [sha256.Size]byte // the block being read
	ctr  [sha256.Size + 4]byte
	next int // the number of the next block
	pos  int // the next unread byte of blk
}

// newShapePool returns a pool of key streams keyed by key. An HMAC state is not
// safe for two goroutines, and the tables of a dump are read in parallel, so each
// call takes a state from the pool.
func newShapePool(key []byte) *sync.Pool {
	return &sync.Pool{New: func() any {
		return &shapeState{h: hmac.New(sha256.New, key)}
	}}
}

// start begins the key stream of a new value at block 0, which is read on first
// use.
func (s *shapeState) start() {
	s.next, s.pos = 0, len(s.blk)
}

// number reads the next width bytes of the key stream of the value as one
// big-endian number. Block 0 is HMAC(key, value); block c is
// SHA-256(block 0 || c). Block 0 is secret (it needs the key), so the later
// blocks need no key of their own, and a long value costs one HMAC over it and
// one SHA-256 of 36 bytes for each block. A number never spans two blocks: the
// rest of a block that is too short for it is skipped.
func (s *shapeState) number(value []byte, width int) uint64 {
	if s.pos+width > len(s.blk) {
		if s.next == 0 {
			s.h.Reset()
			s.h.Write(value)
			s.h.Sum(s.seed[:0])
			s.blk = s.seed
		} else {
			copy(s.ctr[:], s.seed[:])
			binary.BigEndian.PutUint32(s.ctr[sha256.Size:], uint32(s.next))
			s.blk = sha256.Sum256(s.ctr[:])
		}
		s.next++
		s.pos = 0
	}
	var k uint64
	for _, b := range s.blk[s.pos : s.pos+width] {
		k = k<<8 | uint64(b)
	}
	s.pos += width
	return k
}

// shape returns the masked form of raw: the same length in bytes and in
// characters. wideOK says whether characters other than ASCII may be written.
func (s *shapeState) shape(raw []byte, wideOK bool) ([]byte, error) {
	s.start()
	out := make([]byte, 0, len(raw))
	for i := 0; i < len(raw); {
		r, w := utf8.DecodeRune(raw[i:])
		if r == utf8.RuneError && w <= 1 {
			return nil, errShapeUTF8
		}
		i += w
		if r >= utf8.RuneSelf && !wideOK {
			return nil, errShapeCharset
		}
		if r < utf8.RuneSelf && !isASCIIAlnum(r) {
			out = append(out, byte(r)) // punctuation, spaces, control characters
			continue
		}
		lo, hi := shapeRange(r)
		// Two bytes pick a character of a small range (at most 128 characters, so the
		// bias of the remainder is at most 0.2%); a kanji takes three (about 0.13%).
		width := 2
		if hi-lo > 1<<12 {
			width = 3
		}
		k := s.number(raw, width)
		out = utf8.AppendRune(out, lo+rune(k%uint64(hi-lo+1)))
	}
	return out, nil
}

func isASCIIAlnum(r rune) bool {
	return (r >= '0' && r <= '9') || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z')
}

// shapeRange returns the range the character that replaces r is drawn from: the
// range of the class of r. Every range holds assigned characters of one UTF-8
// length.
func shapeRange(r rune) (lo, hi rune) {
	switch {
	case r >= '0' && r <= '9':
		return '0', '9'
	case r >= 'A' && r <= 'Z':
		return 'A', 'Z'
	case r >= 'a' && r <= 'z':
		return 'a', 'z'
	case r >= hiraganaLo && r <= hiraganaInHi:
		return hiraganaLo, hiraganaHi
	case r >= katakanaLo && r <= katakanaInHi:
		return katakanaLo, katakanaHi
	}
	switch utf8.RuneLen(r) {
	case 2:
		return latinXLo, latinXHi
	case 3: // kanji and every other character of three bytes
		return kanjiLo, kanjiOutHi
	default:
		return emojiLo, emojiHi
	}
}
