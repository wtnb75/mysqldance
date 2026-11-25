package mask

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"unicode/utf8"
)

// jsonRewriter rewrites the leaves that the paths of a JSON column reach, and
// copies every other byte of the document as it was: the order of the keys, the
// spaces, the escapes of the other strings and the digits of the numbers. It reads
// the text with a scanner of its own (no decoding into Go values), so a part no
// path goes into is skipped without an allocation.
//
// The scanner checks every token of the document (strings, numbers, true, false,
// null) but, in a part that no path goes into, not where the commas and colons
// are, nor what follows a literal: the server stores only valid JSON, and a part
// that is skipped cannot change what a rewritten leaf is.
type jsonRewriter struct {
	root *pathNode
}

var errJSONSyntax = errors.New("the value is not valid JSON")

// rewrite returns the document with the leaves replaced, and whether it changed.
// An unchanged document is returned as it is (the same slice).
func (r *jsonRewriter) rewrite(doc []byte) ([]byte, bool, error) {
	s := jsonScan{doc: doc}
	var buf [4]*pathNode
	if err := s.value(append(buf[:0], r.root)); err != nil {
		return nil, false, err
	}
	s.skipSpace()
	if s.pos != len(doc) {
		return nil, false, s.syntax()
	}
	// A path counts once for the document, however many values it reached.
	for _, l := range s.hit {
		l.hits.Add(1)
	}
	if s.out == nil {
		return doc, false, nil
	}
	return append(s.out, doc[s.copied:]...), true, nil
}

type jsonScan struct {
	doc []byte
	pos int
	// out holds doc[:copied] with the replacements made so far; nil until the
	// first one.
	out    []byte
	copied int
	enc    []byte // the encoding of a replacement, reused
	// hit holds each path that found a value to transform in the document, once.
	hit []*pathLeaf
}

func (s *jsonScan) syntax() error {
	return fmt.Errorf("%w (at byte %d)", errJSONSyntax, s.pos)
}

func (s *jsonScan) skipSpace() {
	for s.pos < len(s.doc) {
		switch s.doc[s.pos] {
		case ' ', '\t', '\n', '\r':
			s.pos++
		default:
			return
		}
	}
}

// replace puts text in place of doc[from:to].
func (s *jsonScan) replace(from, to int, text []byte) {
	if s.out == nil {
		s.out = make([]byte, 0, len(s.doc)+32)
	}
	s.out = append(s.out, s.doc[s.copied:from]...)
	s.out = append(s.out, text...)
	s.copied = to
}

// value reads the value at the position. active are the nodes of the paths that
// lead to it; with none, the value is skipped.
func (s *jsonScan) value(active []*pathNode) error {
	s.skipSpace()
	if s.pos >= len(s.doc) {
		return s.syntax()
	}
	if len(active) == 0 {
		return s.skipValue()
	}
	// Paths that do not overlap cannot end here and go on, nor two end here, so a
	// leaf is the only node of active.
	for _, n := range active {
		if n.leaf != nil {
			return s.leaf(n)
		}
	}
	switch c := s.doc[s.pos]; c {
	case '{':
		// Paths that share a prefix may go on as an object or as an array ($.a.x and
		// $.a[0]); it is an error only when none goes on as what the value is.
		if !slices.ContainsFunc(active, (*pathNode).expectsObject) {
			return notContainer(active[0], "an array", "an object")
		}
		return s.object(active)
	case '[':
		if !slices.ContainsFunc(active, (*pathNode).expectsArray) {
			return notContainer(active[0], "an object", "an array")
		}
		return s.array(active)
	default:
		kind, err := s.scalarKind()
		if err != nil {
			return err
		}
		if n := active[0]; n.expectsObject() {
			return notContainer(n, "an object", kind.String())
		}
		return notContainer(active[0], "an array", kind.String())
	}
}

func notContainer(n *pathNode, want, got string) error {
	return fmt.Errorf("path %q: a value the path goes into is not %s, it is %s", n.path, want, got)
}

// scalarKind reads the scalar at the position without consuming it.
func (s *jsonScan) scalarKind() (LeafKind, error) {
	save := s.pos
	defer func() { s.pos = save }()
	kind, _, err := s.readScalar()
	return kind, err
}

// leaf reads the value that the path ends at and replaces it.
func (s *jsonScan) leaf(n *pathNode) error {
	start := s.pos
	var in Leaf
	switch s.doc[s.pos] {
	case '{', '[':
		if err := s.skipValue(); err != nil {
			return err
		}
		in.Kind = LeafContainer
	default:
		kind, text, err := s.readScalar()
		if err != nil {
			return err
		}
		in = Leaf{Kind: kind, Text: text}
	}
	end := s.pos
	if in.Kind == LeafNull {
		return nil
	}
	out, err := n.leaf.fn(in)
	if err != nil {
		return fmt.Errorf("path %q: %w", n.path, err)
	}
	enc, err := appendLeaf(s.enc[:0], out)
	if err != nil {
		return fmt.Errorf("path %q: %w", n.path, err)
	}
	s.enc = enc
	if !slices.Contains(s.hit, n.leaf) {
		s.hit = append(s.hit, n.leaf)
	}
	s.replace(start, end, enc)
	return nil
}

// appendLeaf writes a leaf as JSON, with the escapes the syntax needs and no
// others: ", \ and the control characters (as \u00XX).
func appendLeaf(dst []byte, l Leaf) ([]byte, error) {
	switch l.Kind {
	case LeafNull:
		return append(dst, "null"...), nil
	case LeafNumber, LeafBool:
		return append(dst, l.Text...), nil
	case LeafString:
		if !utf8.Valid(l.Text) {
			return nil, errors.New("the new value is not valid UTF-8")
		}
		dst = append(dst, '"')
		const hex = "0123456789abcdef"
		for _, c := range l.Text {
			switch {
			case c == '"' || c == '\\':
				dst = append(dst, '\\', c)
			case c < 0x20:
				dst = append(dst, '\\', 'u', '0', '0', hex[c>>4], hex[c&15])
			default:
				dst = append(dst, c)
			}
		}
		return append(dst, '"'), nil
	default:
		return nil, errors.New("a transform cannot give an object or an array")
	}
}

func (s *jsonScan) object(active []*pathNode) error {
	s.pos++ // {
	s.skipSpace()
	if s.pos < len(s.doc) && s.doc[s.pos] == '}' {
		s.pos++
		return nil
	}
	for {
		s.skipSpace()
		if s.pos >= len(s.doc) || s.doc[s.pos] != '"' {
			return s.syntax()
		}
		from := s.pos
		rawKey, err := s.skipString()
		if err != nil {
			return err
		}
		var buf [4]*pathNode
		child := buf[:0]
		if bytes.IndexByte(rawKey, '\\') < 0 {
			for _, n := range active {
				if c := n.keys[string(rawKey)]; c != nil {
					child = append(child, c)
				}
			}
		} else {
			var key string
			if err := json.Unmarshal(s.doc[from:s.pos], &key); err != nil {
				return s.syntax()
			}
			for _, n := range active {
				if c := n.keys[key]; c != nil {
					child = append(child, c)
				}
			}
		}
		s.skipSpace()
		if s.pos >= len(s.doc) || s.doc[s.pos] != ':' {
			return s.syntax()
		}
		s.pos++
		if err := s.value(child); err != nil {
			return err
		}
		s.skipSpace()
		if s.pos >= len(s.doc) {
			return s.syntax()
		}
		switch s.doc[s.pos] {
		case ',':
			s.pos++
		case '}':
			s.pos++
			return nil
		default:
			return s.syntax()
		}
	}
}

func (s *jsonScan) array(active []*pathNode) error {
	s.pos++ // [
	s.skipSpace()
	if s.pos < len(s.doc) && s.doc[s.pos] == ']' {
		s.pos++
		return nil
	}
	for i := 0; ; i++ {
		var buf [4]*pathNode
		child := buf[:0]
		for _, n := range active {
			if c := n.idx[i]; c != nil {
				child = append(child, c)
			}
			if n.every != nil {
				child = append(child, n.every)
			}
		}
		if err := s.value(child); err != nil {
			return err
		}
		s.skipSpace()
		if s.pos >= len(s.doc) {
			return s.syntax()
		}
		switch s.doc[s.pos] {
		case ',':
			s.pos++
		case ']':
			s.pos++
			return nil
		default:
			return s.syntax()
		}
	}
}

// skipValue skips the value at the position. Inside a container only the tokens
// are checked, not where the commas and colons are.
func (s *jsonScan) skipValue() error {
	depth := 0
	for {
		s.skipSpace()
		if s.pos >= len(s.doc) {
			return s.syntax()
		}
		switch c := s.doc[s.pos]; c {
		case '{', '[':
			depth++
			s.pos++
			continue
		case '}', ']':
			if depth == 0 {
				return s.syntax()
			}
			depth--
			s.pos++
		case ',', ':':
			if depth == 0 {
				return s.syntax()
			}
			s.pos++
			continue
		case '"':
			if _, err := s.skipString(); err != nil {
				return err
			}
		default:
			if _, _, err := s.readScalar(); err != nil {
				return err
			}
		}
		if depth == 0 {
			return nil
		}
	}
}

// skipString skips the string at the position and returns what is between its
// quotes (the escapes as they were written).
func (s *jsonScan) skipString() ([]byte, error) {
	from := s.pos + 1
	for i := from; i < len(s.doc); i++ {
		switch c := s.doc[i]; {
		case c == '"':
			s.pos = i + 1
			return s.doc[from:i], nil
		case c < 0x20:
			s.pos = i
			return nil, s.syntax()
		case c == '\\':
			i++
			if i >= len(s.doc) {
				s.pos = i
				return nil, s.syntax()
			}
			switch s.doc[i] {
			case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
			case 'u':
				if i+4 >= len(s.doc) {
					s.pos = len(s.doc)
					return nil, s.syntax()
				}
				code, ok := hex4(s.doc[i+1 : i+5])
				if !ok {
					s.pos = i
					return nil, s.syntax()
				}
				i += 4
				switch {
				case code >= 0xD800 && code <= 0xDBFF:
					// A high surrogate needs its low one right after it.
					low, ok := uint32(0), false
					if i+6 < len(s.doc) && s.doc[i+1] == '\\' && s.doc[i+2] == 'u' {
						low, ok = hex4(s.doc[i+3 : i+7])
					}
					if !ok || low < 0xDC00 || low > 0xDFFF {
						return nil, fmt.Errorf("%w (a surrogate escape without its pair, at byte %d)", errJSONSyntax, i)
					}
					i += 6
				case code >= 0xDC00 && code <= 0xDFFF:
					return nil, fmt.Errorf("%w (a surrogate escape without its pair, at byte %d)", errJSONSyntax, i)
				}
			default:
				s.pos = i
				return nil, s.syntax()
			}
		}
	}
	s.pos = len(s.doc)
	return nil, s.syntax()
}

// hex4 reads four hexadecimal digits.
func hex4(b []byte) (uint32, bool) {
	var n uint32
	for _, c := range b {
		switch {
		case c >= '0' && c <= '9':
			n = n<<4 | uint32(c-'0')
		case c >= 'a' && c <= 'f':
			n = n<<4 | uint32(c-'a'+10)
		case c >= 'A' && c <= 'F':
			n = n<<4 | uint32(c-'A'+10)
		default:
			return 0, false
		}
	}
	return n, true
}

// readScalar reads the string, number, true, false or null at the position and
// returns its kind and its text (the decoded string, the number as written, or
// true or false).
func (s *jsonScan) readScalar() (LeafKind, []byte, error) {
	switch c := s.doc[s.pos]; {
	case c == '"':
		from := s.pos
		raw, err := s.skipString()
		if err != nil {
			return 0, nil, err
		}
		// Decoding an escaped string would turn a bad byte into U+FFFD, so the text
		// is checked as it was written, with or without escapes.
		if !utf8.Valid(raw) {
			s.pos = from
			return 0, nil, s.syntax()
		}
		if bytes.IndexByte(raw, '\\') < 0 {
			return LeafString, raw, nil
		}
		var str string
		if err := json.Unmarshal(s.doc[from:s.pos], &str); err != nil {
			s.pos = from
			return 0, nil, s.syntax()
		}
		return LeafString, []byte(str), nil
	case c == '-' || (c >= '0' && c <= '9'):
		end, ok := scanNumber(s.doc, s.pos)
		if !ok {
			return 0, nil, s.syntax()
		}
		text := s.doc[s.pos:end]
		s.pos = end
		return LeafNumber, text, nil
	case c == 't':
		return s.literal("true", LeafBool)
	case c == 'f':
		return s.literal("false", LeafBool)
	case c == 'n':
		return s.literal("null", LeafNull)
	default:
		return 0, nil, s.syntax()
	}
}

func (s *jsonScan) literal(word string, kind LeafKind) (LeafKind, []byte, error) {
	if !bytes.HasPrefix(s.doc[s.pos:], []byte(word)) {
		return 0, nil, s.syntax()
	}
	text := s.doc[s.pos : s.pos+len(word)]
	s.pos += len(word)
	if kind == LeafNull {
		return kind, nil, nil
	}
	return kind, text, nil
}

// scanNumber checks the number that starts at b[i] against the JSON grammar
// (-?(0|[1-9][0-9]*)(\.[0-9]+)?([eE][+-]?[0-9]+)?) and returns the offset after it.
func scanNumber(b []byte, i int) (int, bool) {
	digits := func(i int) (int, bool) {
		j := i
		for j < len(b) && b[j] >= '0' && b[j] <= '9' {
			j++
		}
		return j, j > i
	}
	if i < len(b) && b[i] == '-' {
		i++
	}
	if i >= len(b) {
		return 0, false
	}
	if b[i] == '0' {
		i++
	} else if end, ok := digits(i); ok && b[i] != '0' {
		i = end
	} else {
		return 0, false
	}
	if i < len(b) && b[i] == '.' {
		end, ok := digits(i + 1)
		if !ok {
			return 0, false
		}
		i = end
	}
	if i < len(b) && (b[i] == 'e' || b[i] == 'E') {
		i++
		if i < len(b) && (b[i] == '+' || b[i] == '-') {
			i++
		}
		end, ok := digits(i)
		if !ok {
			return 0, false
		}
		i = end
	}
	return i, true
}
