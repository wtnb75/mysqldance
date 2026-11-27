package scan

import (
	"embed"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// builtinFS holds the rule sets that come with the tool. They are gitleaks
// compatible TOML like any rule file, and are used by name through
// [extend] useBuiltin.
//
//go:embed builtin/*.toml
var builtinFS embed.FS

// builtinSets maps the name a rule file uses in useBuiltin to the embedded file.
var builtinSets = map[string]string{
	"pii":         "builtin/pii.toml",
	"pii-columns": "builtin/pii-columns.toml",
}

// BuiltinNames returns the names useBuiltin accepts, sorted.
func BuiltinNames() []string {
	names := make([]string, 0, len(builtinSets))
	for n := range builtinSets {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// WithBuiltin returns a config of the built-in rule sets of names only, the same
// as a rule file that holds nothing but [extend] useBuiltin = names. It is the
// config of a scan that is given no rule file. No name is an error: a config
// without a rule would report that nothing was found.
func WithBuiltin(names ...string) (*Config, error) {
	if len(names) == 0 {
		return nil, errors.New("no built-in rule set named")
	}
	var b strings.Builder
	b.WriteString("[extend]\nuseBuiltin = [")
	for i, n := range names {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(strconv.Quote(n))
	}
	b.WriteString("]\n")
	return Parse([]byte(b.String()))
}

// builtin parses the rule set of the name. The files are part of the program,
// so a file that does not parse is a bug that the tests catch.
func builtin(name string) (*Config, error) {
	data, err := builtinFS.ReadFile(builtinSets[name])
	if err != nil {
		return nil, err
	}
	c, err := parse(data, true)
	if err != nil {
		return nil, fmt.Errorf("built-in rule set %q: %w", name, err)
	}
	return c, nil
}

// applyExtend adds the rule sets that [extend] useBuiltin names to c, and takes
// out the rules of disabledRules. The built-in rules come first, in the order
// of useBuiltin and of each file; the file's own rules follow, and a rule of
// the file replaces a built-in rule that has the same id. A name that is not a
// built-in rule set, and a disabled id that no rule has, are errors: a typo
// must not leave a rule on that the file meant to turn off.
func (p *parser) applyExtend(c *Config, ext extendDoc) {
	if len(ext.UseBuiltin) == 0 && len(ext.DisabledRules) == 0 {
		return
	}
	var rules []*rule
	var global []*allowlist
	seenSet := map[string]bool{}
	for _, name := range ext.UseBuiltin {
		if _, ok := builtinSets[name]; !ok {
			p.errorf("[extend] useBuiltin: unknown built-in rule set %q (known: %s)", name, strings.Join(BuiltinNames(), ", "))
			continue
		}
		if seenSet[name] {
			continue
		}
		seenSet[name] = true
		b, err := builtin(name)
		if err != nil {
			p.errs = append(p.errs, err)
			continue
		}
		rules = append(rules, b.rules...)
		global = append(global, b.global...)
	}
	own := map[string]bool{}
	for _, r := range c.rules {
		own[r.id] = true
	}
	known := map[string]bool{}
	for _, r := range rules {
		known[r.id] = true
	}
	for id := range own {
		known[id] = true
	}
	rules = slices.DeleteFunc(rules, func(r *rule) bool { return own[r.id] })
	rules = append(rules, c.rules...)
	for _, id := range ext.DisabledRules {
		if !known[id] {
			p.errorf("[extend] disabledRules: no rule has the id %q", id)
		}
	}
	rules = slices.DeleteFunc(rules, func(r *rule) bool { return slices.Contains(ext.DisabledRules, r.id) })
	c.rules = rules
	c.global = append(global, c.global...)
}
