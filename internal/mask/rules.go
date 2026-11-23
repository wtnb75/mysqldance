package mask

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// FileVersion is the only version of the rule file this build reads.
const FileVersion = 1

// reservedKeys are the keys of a rule that are not arguments of its transform.
var reservedKeys = map[string]bool{
	"use": true, "domain": true, "when": true, "on_null": true, "on_collision": true, "paths": true,
	"quasi": true,
}

// Rule says how one value is transformed. A rule names a transform (Use) with
// its named Args, or, for a JSON column, gives a rule per JSON path (Paths, in
// file order). Domain, When, OnNull and OnCollision are the keys every
// transform shares. Domain (B-170) names the key the transform uses. The parser
// checks the other three to be strings, and Resolve refuses a rule that has one
// (checkUnsupported) until a later item gives them meaning. Quasi (B-196) marks the column as part of the
// quasi-identifier that scan-k measures; a rule may hold nothing but that, and
// then it has no Use and a dump does nothing for it.
type Rule struct {
	Use         string
	Args        map[string]any
	Domain      string
	When        string
	OnNull      string
	OnCollision string
	Paths       []PathRule
	Quasi       bool
}

// PathRule is the rule for one path inside a JSON value.
type PathRule struct {
	Path string
	Rule Rule
}

// Key names a column: DB.TABLE.COLUMN.
type Key struct {
	DB, Table, Column string
}

func (k Key) String() string { return k.DB + "." + k.Table + "." + k.Column }

// Entry is one rule of the file, with the key as it was written.
type Entry struct {
	Spec string
	Key  Key
	Rule Rule
	Line int
}

// Rules is a parsed rule file; the entries are in file order.
type Rules struct {
	Entries []Entry
}

// SplitKey splits DB.TABLE.COLUMN. The database is the part before the first
// dot and the column the part after the last, as with --exclude-table, so the
// name of a database that contains a dot cannot be written. The table is what
// lies between and may contain dots. All three parts must be non-empty.
func SplitKey(spec string) (Key, error) {
	first := strings.Index(spec, ".")
	last := strings.LastIndex(spec, ".")
	if first < 0 || first == last {
		return Key{}, fmt.Errorf("%q is not of the form DB.TABLE.COLUMN", spec)
	}
	k := Key{DB: spec[:first], Table: spec[first+1 : last], Column: spec[last+1:]}
	if k.DB == "" || k.Table == "" || k.Column == "" {
		return Key{}, fmt.Errorf("%q is not of the form DB.TABLE.COLUMN", spec)
	}
	return k, nil
}

// Load reads the rule file at path and parses it; see Parse.
func Load(path string, reg *Registry) (*Rules, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	rules, err := Parse(data, reg)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return rules, nil
}

// Parse reads a rule file. The version must be FileVersion, and a key, a
// transform name or an argument name that is not known is an error. Every
// problem found is reported together (joined, one per line), so a file is fixed
// in one pass. The rules are not yet checked against the database; see
// Rules.Validate.
func Parse(data []byte, reg *Registry) (*Rules, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	var doc yaml.Node
	if err := dec.Decode(&doc); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, errors.New("the rule file is empty")
		}
		return nil, fmt.Errorf("invalid YAML: %w", err)
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); err == nil {
		return nil, errors.New("the rule file holds more than one YAML document")
	} else if !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("invalid YAML: %w", err)
	}
	root := &doc
	if root.Kind == yaml.DocumentNode && len(root.Content) == 1 {
		root = root.Content[0]
	}
	p := &parser{reg: reg}
	rules := p.parseRoot(root)
	if err := errors.Join(p.errs...); err != nil {
		return nil, err
	}
	return rules, nil
}

type parser struct {
	reg  *Registry
	errs []error
}

func (p *parser) errorf(n *yaml.Node, format string, args ...any) {
	p.errs = append(p.errs, fmt.Errorf("line %d: %s", n.Line, fmt.Sprintf(format, args...)))
}

// pairs returns the key/value nodes of a mapping, and reports a key that is not
// a plain string and a key that appears twice (yaml.v3 only rejects the latter
// when decoding into a Go map).
func (p *parser) pairs(n *yaml.Node, what string) (keys, vals []*yaml.Node) {
	if n.Kind != yaml.MappingNode {
		p.errorf(n, "%s must be a mapping", what)
		return nil, nil
	}
	seen := map[string]bool{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		if k.Kind != yaml.ScalarNode || k.Tag != "!!str" {
			p.errorf(k, "a key of %s must be a string", what)
			continue
		}
		if seen[k.Value] {
			p.errorf(k, "key %q appears twice in %s", k.Value, what)
			continue
		}
		seen[k.Value] = true
		keys = append(keys, k)
		vals = append(vals, v)
	}
	return keys, vals
}

func (p *parser) parseRoot(root *yaml.Node) *Rules {
	rules := &Rules{}
	keys, vals := p.pairs(root, "the rule file")
	haveVersion := false
	haveRules := false
	for i, k := range keys {
		v := vals[i]
		switch k.Value {
		case "version":
			haveVersion = true
			if v.Kind != yaml.ScalarNode || v.Tag != "!!int" || v.Value != fmt.Sprint(FileVersion) {
				p.errorf(v, "unsupported version %q; this build reads version %d", v.Value, FileVersion)
			}
		case "rules":
			haveRules = true
			p.parseRules(v, rules)
		default:
			p.errorf(k, "unknown key %q at the top level (known: version, rules)", k.Value)
		}
	}
	if root.Kind == yaml.MappingNode {
		if !haveVersion {
			p.errorf(root, "version is required (version: %d)", FileVersion)
		}
		if !haveRules {
			p.errorf(root, "rules is required")
		}
	}
	return rules
}

func (p *parser) parseRules(n *yaml.Node, out *Rules) {
	if n.Kind == yaml.ScalarNode && n.Tag == "!!null" {
		return
	}
	keys, vals := p.pairs(n, "rules")
	for i, k := range keys {
		key, err := SplitKey(k.Value)
		if err != nil {
			p.errorf(k, "rules: %v", err)
			continue
		}
		rule := p.parseRule(vals[i], "rule "+k.Value, false)
		out.Entries = append(out.Entries, Entry{Spec: k.Value, Key: key, Rule: rule, Line: k.Line})
	}
}

// parseRule reads one rule. A transform that is not registered is reported, and
// its arguments are then not checked (there is nothing to check them against).
func (p *parser) parseRule(n *yaml.Node, what string, nested bool) Rule {
	var r Rule
	keys, vals := p.pairs(n, what)
	var tr Transform
	trKnown := false
	var useNode *yaml.Node
	for i, k := range keys {
		if k.Value != "use" {
			continue
		}
		useNode = vals[i]
		if useNode.Kind != yaml.ScalarNode || useNode.Tag != "!!str" {
			p.errorf(useNode, "%s: use must be a string", what)
			continue
		}
		r.Use = useNode.Value
		if t, ok := p.reg.Lookup(r.Use); ok {
			tr, trKnown = t, true
		} else {
			p.errorf(useNode, "%s: unknown transform %q (known: %s)", what, r.Use, strings.Join(p.reg.Names(), ", "))
		}
	}
	seenArgs := map[string]bool{}
	for i, k := range keys {
		v := vals[i]
		switch k.Value {
		case "use":
		case "quasi":
			if nested {
				p.errorf(k, "%s: quasi cannot be used inside a path rule", what)
				continue
			}
			if v.Kind != yaml.ScalarNode || v.Tag != "!!bool" {
				p.errorf(v, "%s: quasi must be a boolean (true or false)", what)
				continue
			}
			r.Quasi = v.Value == "true"
		case "domain", "when", "on_null", "on_collision":
			if v.Kind != yaml.ScalarNode || v.Tag != "!!str" {
				p.errorf(v, "%s: %s must be a string", what, k.Value)
				continue
			}
			switch k.Value {
			case "domain":
				if err := checkDomainName(v.Value); err != nil {
					p.errorf(v, "%s: %v", what, err)
					continue
				}
				r.Domain = v.Value
			case "when":
				r.When = v.Value
			case "on_null":
				r.OnNull = v.Value
			case "on_collision":
				r.OnCollision = v.Value
			}
		case "paths":
			if nested {
				p.errorf(k, "%s: paths cannot be nested inside a path rule", what)
				continue
			}
			pk, pv := p.pairs(v, what+": paths")
			for j, kn := range pk {
				if strings.TrimSpace(kn.Value) == "" {
					p.errorf(kn, "%s: a path must not be empty", what)
					continue
				}
				r.Paths = append(r.Paths, PathRule{Path: kn.Value, Rule: p.parseRule(pv[j], what+" path "+kn.Value, true)})
			}
		default:
			if !trKnown {
				// With a transform named but unknown, its arguments cannot be checked.
				if useNode == nil {
					p.errorf(k, "%s: unknown key %q", what, k.Value)
				}
				continue
			}
			def, ok := tr.arg(k.Value)
			if !ok {
				p.errorf(k, "%s: transform %q has no argument %q", what, r.Use, k.Value)
				continue
			}
			seenArgs[k.Value] = true
			if val, ok := p.argValue(v, def, what); ok {
				if r.Args == nil {
					r.Args = map[string]any{}
				}
				r.Args[k.Value] = val
			}
		}
	}
	if trKnown {
		for _, def := range tr.Args {
			if def.Required && !seenArgs[def.Name] {
				p.errorf(n, "%s: transform %q needs argument %q", what, r.Use, def.Name)
			}
		}
	}
	hasPaths := len(r.Paths) > 0
	switch {
	case r.Quasi && hasPaths:
		p.errorf(n, "%s: quasi cannot be combined with paths", what)
	case useNode == nil && !hasPaths && !r.Quasi && n.Kind == yaml.MappingNode:
		p.errorf(n, "%s: use is required (or paths, for a JSON column, or quasi: true)", what)
	case useNode != nil && hasPaths:
		p.errorf(n, "%s: use and paths cannot be combined", what)
	}
	return r
}

// argValue decodes an argument and checks it is of the kind declared.
func (p *parser) argValue(v *yaml.Node, def ArgDef, what string) (any, bool) {
	ok := true
	switch def.Kind {
	case ArgString:
		ok = v.Kind == yaml.ScalarNode && v.Tag == "!!str"
	case ArgInt:
		ok = v.Kind == yaml.ScalarNode && v.Tag == "!!int"
	case ArgBool:
		ok = v.Kind == yaml.ScalarNode && v.Tag == "!!bool"
	case ArgScalar:
		// An unquoted 2020-01-02 is a !!timestamp; the date columns take it as the
		// string it was written as.
		ok = v.Kind == yaml.ScalarNode && (v.Tag == "!!str" || v.Tag == "!!int" || v.Tag == "!!float" || v.Tag == "!!bool" || v.Tag == "!!timestamp")
	}
	if !ok {
		p.errorf(v, "%s: argument %q must be a %s", what, def.Name, def.Kind)
		return nil, false
	}
	if def.Kind == ArgScalar {
		s, err := newScalar(v)
		if err != nil {
			p.errorf(v, "%s: argument %q: %v", what, def.Name, err)
			return nil, false
		}
		return s, true
	}
	if def.Kind == ArgInt {
		// One Go type for every integer, and a value out of range is an error here.
		n, err := strconv.ParseInt(v.Value, 0, 64)
		if err != nil {
			p.errorf(v, "%s: argument %q: %q is not a 64-bit integer", what, def.Name, v.Value)
			return nil, false
		}
		return n, true
	}
	var out any
	if err := v.Decode(&out); err != nil {
		p.errorf(v, "%s: argument %q: %v", what, def.Name, err)
		return nil, false
	}
	return out, true
}
