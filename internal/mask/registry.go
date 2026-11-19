// Package mask reads and checks the rules that say how column values are
// transformed before they are written (B-165). It holds the rule file, the
// registry of transforms and the salt; the transforms themselves are
// registered by later items.
package mask

import (
	"fmt"
	"slices"
	"sort"
	"strings"
)

// ArgKind is the kind of value a named argument of a transform takes.
type ArgKind int

// The kinds an argument can take.
const (
	ArgString ArgKind = iota
	ArgInt
	ArgBool
	ArgAny
)

func (k ArgKind) String() string {
	switch k {
	case ArgString:
		return "string"
	case ArgInt:
		return "integer"
	case ArgBool:
		return "boolean"
	default:
		return "any"
	}
}

// ArgDef declares one named argument of a transform.
type ArgDef struct {
	Name     string
	Kind     ArgKind
	Required bool
}

// Transform declares a transform: its name, its named arguments, and the column
// types it applies to. Types are the driver's type names (sqlvalue.Column
// TypeName, for example VARCHAR or JSON), compared without the UNSIGNED prefix;
// an empty list means every type.
type Transform struct {
	Name  string
	Args  []ArgDef
	Types []string
}

// appliesTo reports whether the transform can be used on a column of the type.
func (t Transform) appliesTo(typeName string) bool {
	if len(t.Types) == 0 {
		return true
	}
	return slices.Contains(t.Types, normalizeType(typeName))
}

func (t Transform) arg(name string) (ArgDef, bool) {
	for _, a := range t.Args {
		if a.Name == name {
			return a, true
		}
	}
	return ArgDef{}, false
}

// Registry is the set of transforms a rule file may name.
type Registry struct {
	byName map[string]Transform
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{byName: map[string]Transform{}}
}

// Register adds a transform. A name that is empty or already registered, or an
// argument declared twice or named like a key every rule has, is an error.
func (r *Registry) Register(t Transform) error {
	if t.Name == "" {
		return fmt.Errorf("transform with no name")
	}
	if _, dup := r.byName[t.Name]; dup {
		return fmt.Errorf("transform %q is registered twice", t.Name)
	}
	seen := map[string]bool{}
	for _, a := range t.Args {
		if reservedKeys[a.Name] {
			return fmt.Errorf("transform %q: argument %q is a key every rule has", t.Name, a.Name)
		}
		if seen[a.Name] {
			return fmt.Errorf("transform %q: argument %q is declared twice", t.Name, a.Name)
		}
		seen[a.Name] = true
	}
	// Types are kept in the form appliesTo compares: upper case, no UNSIGNED.
	types := make([]string, len(t.Types))
	for i, ty := range t.Types {
		types[i] = normalizeType(ty)
	}
	t.Types = types
	r.byName[t.Name] = t
	return nil
}

// normalizeType puts a column type name in the form Types are compared in.
func normalizeType(name string) string {
	return strings.TrimPrefix(strings.ToUpper(strings.TrimSpace(name)), "UNSIGNED ")
}

// Lookup returns the transform of the name. A nil registry has none.
func (r *Registry) Lookup(name string) (Transform, bool) {
	if r == nil {
		return Transform{}, false
	}
	t, ok := r.byName[name]
	return t, ok
}

// Names returns the registered names in sorted order.
func (r *Registry) Names() []string {
	if r == nil {
		return nil
	}
	names := make([]string, 0, len(r.byName))
	for n := range r.byName {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
