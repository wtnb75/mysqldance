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
	// ArgScalar takes a string, an integer, a decimal number or a boolean, and
	// keeps its text (see Scalar), so that no digit is lost on the way.
	ArgScalar
)

func (k ArgKind) String() string {
	switch k {
	case ArgString:
		return "string"
	case ArgInt:
		return "integer"
	case ArgBool:
		return "boolean"
	case ArgScalar:
		return "string, number or boolean"
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
	// Version is the version of what the transform outputs, from 1. It is part of
	// the HMAC key (DeriveKey): a change that gives other values for the same
	// input is a new version, so that updating the tool does not change a dump.
	Version int
	// NeedsSalt is true for a transform that uses the salt. A run that uses one
	// without a salt is refused.
	NeedsSalt bool
	// BreaksKeys is true for a transform that can give two rows one value or a
	// value that no parent row has, so that it cannot be used on a column of a
	// key (the ordering key, a unique index, the child side of a foreign key).
	BreaksKeys bool
	// NoChange is true for a transform that leaves every value as it is (keep). It
	// only records that the column was looked at, so it costs nothing per row and
	// may be used on any column, a column of a key included.
	NoChange bool
	// Build prepares the transform for one rule: it checks the arguments against
	// the definition of the column and returns the Func that is applied to each
	// row. A nil Build is for a transform that is only registered (tests).
	Build func(BuildContext) (Func, error)
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
	if t.Version < 0 || (t.NeedsSalt && t.Version < 1) {
		return fmt.Errorf("transform %q: the version of a transform that uses the salt starts at 1", t.Name)
	}
	if t.NoChange && (len(t.Args) > 0 || t.NeedsSalt || t.Build != nil) {
		return fmt.Errorf("transform %q: a transform that changes nothing takes no argument, salt or Build", t.Name)
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
