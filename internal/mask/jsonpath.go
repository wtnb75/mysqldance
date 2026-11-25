package mask

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
)

// The paths a rule can give for a JSON column (B-169) are a small subset of
// JSONPath: "$" and then any number of
//
//	.key         a key of letters, digits and "_", not starting with a digit
//	["key"]      any key, written as a JSON string
//	[*]          every element of an array
//	[n]          element n of an array, counting from 0
//
// Filters, recursion (".."), slices, a negative index and a path that is only
// "$" are errors in the file.

type stepKind int

const (
	stepKey stepKind = iota
	stepIndex
	stepWildcard
)

type pathStep struct {
	kind  stepKind
	key   string
	index int
}

// maxPathIndex bounds the index of a step, so that it fits an int on every
// platform and a typo of many digits is an error.
const (
	maxPathIndex       = 999999999
	maxPathIndexDigits = 9
)

func isKeyStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isKeyChar(c byte) bool { return isKeyStart(c) || (c >= '0' && c <= '9') }

// parseJSONPath reads a path of a rule. The error says what is wrong with it,
// never anything but the path (which the rule file holds).
func parseJSONPath(p string) ([]pathStep, error) {
	if !strings.HasPrefix(p, "$") {
		return nil, errors.New(`a path must start with "$"`)
	}
	if p == "$" {
		return nil, errors.New(`a path must point inside the document: "$" alone is the whole value`)
	}
	var steps []pathStep
	for i := 1; i < len(p); {
		switch p[i] {
		case '.':
			if i+1 < len(p) && p[i+1] == '.' {
				return nil, errors.New(`recursive descent ("..") is not supported`)
			}
			j := i + 1
			if j >= len(p) || !isKeyStart(p[j]) {
				return nil, fmt.Errorf("after \".\" (at offset %d) a key is expected: letters, digits and \"_\", not starting with a digit (write other keys as [\"key\"])", i)
			}
			for j < len(p) && isKeyChar(p[j]) {
				j++
			}
			steps = append(steps, pathStep{kind: stepKey, key: p[i+1 : j]})
			i = j
		case '[':
			step, next, err := parseBracket(p, i)
			if err != nil {
				return nil, err
			}
			steps = append(steps, step)
			i = next
		default:
			return nil, fmt.Errorf("unexpected %q at offset %d", p[i], i)
		}
	}
	return steps, nil
}

// parseBracket reads the step that starts with the "[" at p[i] and returns it
// with the offset after its "]".
func parseBracket(p string, i int) (pathStep, int, error) {
	j := i + 1
	if j >= len(p) {
		return pathStep{}, 0, errors.New(`unterminated "["`)
	}
	switch c := p[j]; {
	case c == '*':
		if j+1 >= len(p) || p[j+1] != ']' {
			return pathStep{}, 0, errors.New(`unterminated "[*"`)
		}
		return pathStep{kind: stepWildcard}, j + 2, nil
	case c == '"':
		k := j + 1
		for k < len(p) && p[k] != '"' {
			if p[k] == '\\' {
				k++
			}
			k++
		}
		if k >= len(p) {
			return pathStep{}, 0, errors.New(`unterminated key string after "["`)
		}
		var key string
		if err := json.Unmarshal([]byte(p[j:k+1]), &key); err != nil {
			return pathStep{}, 0, errors.New("the key string is not a valid JSON string")
		}
		if k+1 >= len(p) || p[k+1] != ']' {
			return pathStep{}, 0, errors.New(`unterminated "[": a key string must be followed by "]"`)
		}
		return pathStep{kind: stepKey, key: key}, k + 2, nil
	case c == '\'':
		return pathStep{}, 0, errors.New(`a key in brackets is written with double quotes: ["key"]`)
	case c == '-':
		return pathStep{}, 0, errors.New("a negative index is not supported")
	case c == '?':
		return pathStep{}, 0, errors.New("a filter is not supported")
	case c >= '0' && c <= '9':
		k := j
		for k < len(p) && p[k] >= '0' && p[k] <= '9' {
			k++
		}
		if k < len(p) && (p[k] == ':' || p[k] == ',') {
			return pathStep{}, 0, errors.New("a slice or a list of indexes is not supported")
		}
		if k >= len(p) {
			return pathStep{}, 0, errors.New(`unterminated "["`)
		}
		if p[k] != ']' {
			return pathStep{}, 0, fmt.Errorf("unexpected %q at offset %d", p[k], k)
		}
		digits := p[j:k]
		if len(digits) > 1 && digits[0] == '0' {
			return pathStep{}, 0, errors.New("an index has no leading zero")
		}
		if len(digits) > maxPathIndexDigits {
			return pathStep{}, 0, fmt.Errorf("the index is too large (at most %d)", maxPathIndex)
		}
		n := 0
		for _, d := range []byte(digits) {
			n = n*10 + int(d-'0')
		}
		return pathStep{kind: stepIndex, index: n}, k + 1, nil
	default:
		return pathStep{}, 0, fmt.Errorf("unexpected %q at offset %d: a [ is followed by *, an index or a key string", c, j)
	}
}

// stepsCompatible reports whether some JSON value can be reached by both steps
// at the same place: the same key, the same index, or a wildcard against an index
// or a wildcard. A key and an index never meet: no value is both an object and
// an array.
func stepsCompatible(a, b pathStep) bool {
	switch {
	case a.kind == stepKey || b.kind == stepKey:
		return a.kind == stepKey && b.kind == stepKey && a.key == b.key
	case a.kind == stepWildcard || b.kind == stepWildcard:
		return true
	default:
		return a.index == b.index
	}
}

// pathsOverlap reports whether one path points to a value that the other points
// into or at: every step of the shorter one is compatible with the one of the
// longer one at the same place ($.a and $.a.b; $.p[*] and $.p[0]).
func pathsOverlap(a, b []pathStep) bool {
	n := min(len(a), len(b))
	for i := range n {
		if !stepsCompatible(a[i], b[i]) {
			return false
		}
	}
	return true
}

// pathNode is a node of the tree the paths of one column make: the children
// that a JSON object (keys) or array (idx, every) is followed into, and, where a
// path ends, the leaf rule. path is one of the paths through the node, for the
// messages.
type pathNode struct {
	keys  map[string]*pathNode
	idx   map[int]*pathNode
	every *pathNode
	leaf  *pathLeaf
	path  string
}

// pathLeaf is the rule at the end of a path.
type pathLeaf struct {
	fn LeafFunc
	// path and use name the rule for the usage report. hits counts the rows where
	// the path found a value to transform.
	path, use string
	hits      atomic.Int64
}

func (n *pathNode) child(step pathStep, path string) *pathNode {
	var c *pathNode
	switch step.kind {
	case stepKey:
		if n.keys == nil {
			n.keys = map[string]*pathNode{}
		}
		if c = n.keys[step.key]; c == nil {
			c = &pathNode{path: path}
			n.keys[step.key] = c
		}
	case stepIndex:
		if n.idx == nil {
			n.idx = map[int]*pathNode{}
		}
		if c = n.idx[step.index]; c == nil {
			c = &pathNode{path: path}
			n.idx[step.index] = c
		}
	default:
		if n.every == nil {
			n.every = &pathNode{path: path}
		}
		c = n.every
	}
	return c
}

// add puts the leaf at the end of the steps. The paths must not overlap.
func (n *pathNode) add(steps []pathStep, path string, leaf *pathLeaf) {
	for _, s := range steps {
		n = n.child(s, path)
	}
	n.leaf = leaf
}

func (n *pathNode) expectsObject() bool { return len(n.keys) > 0 }
func (n *pathNode) expectsArray() bool  { return len(n.idx) > 0 || n.every != nil }
