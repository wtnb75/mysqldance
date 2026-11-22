// Package scan reads gitleaks-compatible detection rules and applies them to
// the value of one column. It finds places where a value looks like personal
// information or a secret, so that they can be masked.
//
// The rules are the TOML of gitleaks 8.x, read for the keys that decide what
// is detected; see Parse for the exact set. A rule's path is matched against
// DB.TABLE.COLUMN instead of a file path.
//
// A finding says which rule matched and where in the value, never the matched
// text, so that a report cannot leak what it found.
package scan

import (
	"errors"
	"fmt"
	"math"
	"os"
	"regexp"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"
)

// Match is one finding in a value: the id of the rule and the byte range of
// the secret within the value. The secret is the capture group named by the
// rule's secretGroup (or the first non-empty group, or the whole match). For a
// rule that has only a path, the range is the whole value.
type Match struct {
	RuleID string
	Start  int
	Length int
}

// Config is a parsed rule file.
type Config struct {
	rules  []*rule
	global []*allowlist
}

type rule struct {
	id          string
	regex       *regexp.Regexp // nil for a rule that has only a path
	path        *regexp.Regexp // nil if the rule has no path
	keywords    []string       // lower case
	entropy     float64
	secretGroup int
	validate    func(secret string) bool // nil: no check
	allow       []*allowlist
}

type allowlist struct {
	and       bool
	target    string // "secret", "match" or "line": what the regexes are matched against
	paths     []*regexp.Regexp
	regexes   []*regexp.Regexp
	stopwords []string // lower case
}

// The structs below mirror the file. A key that is not a field here is
// reported by Parse, so a rule is never silently weakened by a key that this
// build does not understand.

type fileDoc struct {
	// Display only: read and dropped.
	Title      string `toml:"title"`
	MinVersion string `toml:"minVersion"`

	Extend     *extendDoc     `toml:"extend"`
	Allowlist  *allowlistDoc  `toml:"allowlist"`
	Allowlists []allowlistDoc `toml:"allowlists"`
	Rules      []ruleDoc      `toml:"rules"`
}

// extendDoc is the [extend] table. UseBuiltin and DisabledRules are read (see
// builtin.go). UseDefault and Path are gitleaks keys that this build does not
// support yet (B-204); they are decoded only so that they can be named in the error.
type extendDoc struct {
	UseBuiltin    []string `toml:"useBuiltin"`
	DisabledRules []string `toml:"disabledRules"`
	UseDefault    any      `toml:"useDefault"`
	Path          any      `toml:"path"`
}

type ruleDoc struct {
	ID          string   `toml:"id"`
	Regex       string   `toml:"regex"`
	Path        string   `toml:"path"`
	Keywords    []string `toml:"keywords"`
	Entropy     float64  `toml:"entropy"`
	SecretGroup int      `toml:"secretGroup"`
	// Validate is not a gitleaks key: it names a check (see validate.go) that a
	// match must pass to count. gitleaks ignores it, so a file that uses it can
	// only give more detections there.
	// A pointer, so that validate = "" is seen as written and refused, not read as absent.
	Validate *string `toml:"validate"`

	Allowlist  *allowlistDoc  `toml:"allowlist"`
	Allowlists []allowlistDoc `toml:"allowlists"`

	// Display only: read and dropped.
	Description string   `toml:"description"`
	Tags        []string `toml:"tags"`
}

type allowlistDoc struct {
	Condition   string   `toml:"condition"`
	RegexTarget string   `toml:"regexTarget"`
	Paths       []string `toml:"paths"`
	Regexes     []string `toml:"regexes"`
	Stopwords   []string `toml:"stopwords"`

	// Display only: read and dropped.
	Description string `toml:"description"`
}

// Load reads the rule file at path and parses it; see Parse.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	c, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

// Parse reads a gitleaks-compatible rule file. The keys it understands are
// those the standard gitleaks.toml uses to decide a detection:
//
//   - top level: title and minVersion (read and dropped), allowlist and
//     allowlists (the global allowlists), rules;
//   - a rule: id (required), regex, path, keywords, entropy, secretGroup,
//     allowlist and allowlists; description and tags are read and dropped;
//   - an allowlist: regexes, regexTarget (secret, match or line), stopwords,
//     paths and condition (OR or AND); description is read and dropped.
//
// Any other key is an error that names the key: the file may be written for a
// feature that this build does not have (commits, targetRules, composite
// rules, CEL expressions), and ignoring it would weaken a detection without a
// word. Of [extend], useBuiltin and disabledRules are read (see builtin.go);
// useDefault and path are not supported yet (B-204). A rule may also have the
// key validate, which is not gitleaks'. Every problem is reported together,
// one per line, each with the id of its rule.
func Parse(data []byte) (*Config, error) { return parse(data, false) }

// parse is Parse; builtin is set for a rule set of this package, which cannot
// extend anything.
func parse(data []byte, builtin bool) (*Config, error) {
	var doc fileDoc
	md, err := toml.Decode(string(data), &doc)
	if err != nil {
		return nil, fmt.Errorf("invalid TOML: %w", err)
	}
	p := &parser{}
	for _, k := range md.Undecoded() {
		p.errorf("unsupported key %q", k.String())
	}
	var ext extendDoc
	if doc.Extend != nil {
		ext = *doc.Extend
		if builtin {
			p.errorf("[extend] cannot be used in a built-in rule set")
		}
		for _, k := range []string{"useDefault", "path"} {
			if md.IsDefined("extend", k) {
				p.errorf("[extend] %s is not supported yet (planned in B-204); put the rules in this file", k)
			}
		}
	}
	c := &Config{}
	if doc.Allowlist != nil {
		c.global = append(c.global, p.allowlist(*doc.Allowlist, "allowlist"))
	}
	for i, a := range doc.Allowlists {
		c.global = append(c.global, p.allowlist(a, fmt.Sprintf("allowlists #%d", i+1)))
	}
	seen := map[string]bool{}
	for i, rd := range doc.Rules {
		label := fmt.Sprintf("rule #%d", i+1)
		if rd.ID == "" {
			p.errorf("%s: id is required", label)
		} else {
			label = fmt.Sprintf("rule %q", rd.ID)
			if seen[rd.ID] {
				p.errorf("%s: id appears twice", label)
			}
			seen[rd.ID] = true
		}
		c.rules = append(c.rules, p.rule(rd, label))
	}
	p.applyExtend(c, ext)
	if len(c.rules) == 0 {
		p.errorf("the config has no rules")
	}
	if err := errors.Join(p.errs...); err != nil {
		return nil, err
	}
	return c, nil
}

type parser struct {
	errs []error
}

func (p *parser) errorf(format string, args ...any) {
	p.errs = append(p.errs, fmt.Errorf(format, args...))
}

func (p *parser) compile(expr, what string) *regexp.Regexp {
	re, err := regexp.Compile(expr)
	if err != nil {
		p.errorf("%s: invalid regex: %v", what, err)
		return nil
	}
	return re
}

func (p *parser) rule(rd ruleDoc, label string) *rule {
	r := &rule{id: rd.ID, entropy: rd.Entropy, secretGroup: rd.SecretGroup}
	if rd.Regex != "" {
		r.regex = p.compile(rd.Regex, label+": regex")
	}
	if rd.Path != "" {
		r.path = p.compile(rd.Path, label+": path")
	}
	if rd.Regex == "" && rd.Path == "" {
		p.errorf("%s: needs a regex or a path", label)
	}
	for _, k := range rd.Keywords {
		r.keywords = append(r.keywords, strings.ToLower(k))
	}
	if rd.Entropy < 0 {
		p.errorf("%s: entropy must not be negative", label)
	}
	groups := 0
	if r.regex != nil {
		groups = r.regex.NumSubexp()
	}
	if rd.SecretGroup < 0 || rd.SecretGroup > groups {
		p.errorf("%s: secretGroup %d is out of range (the regex has %d capture groups)", label, rd.SecretGroup, groups)
	}
	if rd.Validate != nil {
		if rd.Regex == "" {
			p.errorf("%s: validate needs a regex (it checks the secret that the regex found)", label)
		}
		if fn, ok := validators[*rd.Validate]; ok {
			r.validate = fn
		} else {
			p.errorf("%s: unknown validate %q (known: %s)", label, *rd.Validate, strings.Join(validatorNames(), ", "))
		}
	}
	if rd.Allowlist != nil {
		r.allow = append(r.allow, p.allowlist(*rd.Allowlist, label+": allowlist"))
	}
	for i, a := range rd.Allowlists {
		r.allow = append(r.allow, p.allowlist(a, fmt.Sprintf("%s: allowlists #%d", label, i+1)))
	}
	return r
}

func (p *parser) allowlist(ad allowlistDoc, label string) *allowlist {
	a := &allowlist{target: "secret"}
	switch strings.ToUpper(ad.Condition) {
	case "", "OR":
	case "AND":
		a.and = true
	default:
		p.errorf("%s: condition must be OR or AND, got %q", label, ad.Condition)
	}
	switch ad.RegexTarget {
	case "":
	case "secret", "match", "line":
		a.target = ad.RegexTarget
	default:
		p.errorf("%s: regexTarget must be secret, match or line, got %q", label, ad.RegexTarget)
	}
	for _, s := range ad.Paths {
		if re := p.compile(s, label+": paths"); re != nil {
			a.paths = append(a.paths, re)
		}
	}
	for _, s := range ad.Regexes {
		if re := p.compile(s, label+": regexes"); re != nil {
			a.regexes = append(a.regexes, re)
		}
	}
	for _, s := range ad.Stopwords {
		a.stopwords = append(a.stopwords, strings.ToLower(s))
	}
	if len(ad.Paths)+len(ad.Regexes)+len(ad.Stopwords) == 0 {
		p.errorf("%s: needs at least one of paths, regexes or stopwords", label)
	}
	return a
}

// allowed reports whether the finding is allowlisted. With condition OR one
// criterion that holds is enough; with AND every criterion the allowlist
// names must hold. Stopwords are checked against the secret.
func (a *allowlist) allowed(path, secret, match string, line func() string) bool {
	hit := func(res []*regexp.Regexp, s string) bool {
		return slices.ContainsFunc(res, func(re *regexp.Regexp) bool { return re.MatchString(s) })
	}
	target := secret
	switch a.target {
	case "match":
		target = match
	case "line":
		// The caller passes the lines the match touches (see matchLines), or
		// the whole value for a rule without a regex (spec 20.2, R-204).
		target = line()
	}
	var checks []bool
	if len(a.paths) > 0 {
		checks = append(checks, hit(a.paths, path))
	}
	if len(a.regexes) > 0 {
		checks = append(checks, hit(a.regexes, target))
	}
	if len(a.stopwords) > 0 {
		lower := strings.ToLower(secret)
		checks = append(checks, slices.ContainsFunc(a.stopwords, func(w string) bool { return strings.Contains(lower, w) }))
	}
	if a.and {
		return len(checks) > 0 && !slices.Contains(checks, false)
	}
	return slices.Contains(checks, true)
}

// Scan applies the rules to one value. path is DB.TABLE.COLUMN. The findings
// are in the order of the rules in the file, and within a rule in the order of
// position, so the result is the same on every run.
func (c *Config) Scan(path, value string) []Match {
	if value == "" {
		return nil
	}
	var lower string
	var out []Match
	for _, r := range c.rules {
		// The path is short and the cheapest test, so it goes before the
		// keywords, which lower-case the whole value.
		if r.path != nil && !r.path.MatchString(path) {
			continue
		}
		if len(r.keywords) > 0 {
			if lower == "" {
				lower = strings.ToLower(value)
			}
			if !slices.ContainsFunc(r.keywords, func(k string) bool { return strings.Contains(lower, k) }) {
				continue
			}
		}
		if r.regex == nil {
			// A rule with only a path flags the whole column.
			if !c.allowed(r, path, value, value, func() string { return value }) {
				out = append(out, Match{RuleID: r.id, Start: 0, Length: len(value)})
			}
			continue
		}
		for _, loc := range r.regex.FindAllStringSubmatchIndex(value, -1) {
			start, end := loc[0], loc[1]
			// The secret is the chosen group, else the first non-empty group.
			// A group that is absent or empty never hides a finding: the
			// whole match is reported instead.
			if g := r.secretGroup; g > 0 {
				if loc[2*g] >= 0 && loc[2*g+1] > loc[2*g] {
					start, end = loc[2*g], loc[2*g+1]
				}
			} else {
				for i := 2; i+1 < len(loc); i += 2 {
					if loc[i] >= 0 && loc[i+1] > loc[i] {
						start, end = loc[i], loc[i+1]
						break
					}
				}
			}
			if end <= start {
				continue
			}
			secret := value[start:end]
			if r.entropy != 0 && shannonEntropy(secret) <= r.entropy {
				continue
			}
			if r.validate != nil && !r.validate(secret) {
				continue
			}
			if c.allowed(r, path, secret, value[loc[0]:loc[1]], func() string { return matchLines(value, loc[0], loc[1]) }) {
				continue
			}
			out = append(out, Match{RuleID: r.id, Start: start, Length: end - start})
		}
	}
	return out
}

// matchLines is the "line" an allowlist with regexTarget = "line" sees: every
// line of value that the match [start, end) touches, from the line holding its
// first byte to the line holding its last byte. Lines are split at "\n" only;
// a "\r" stays in the line. A match that ends with "\n" belongs to the line
// that "\n" ends, so the next line is not pulled in.
func matchLines(value string, start, end int) string {
	last := max(end-1, start)
	from := strings.LastIndexByte(value[:start], '\n') + 1
	to := len(value)
	if i := strings.IndexByte(value[last:], '\n'); i >= 0 {
		to = last + i
	}
	return value[from:to]
}

func (c *Config) allowed(r *rule, path, secret, match string, line func() string) bool {
	for _, a := range r.allow {
		if a.allowed(path, secret, match, line) {
			return true
		}
	}
	for _, a := range c.global {
		if a.allowed(path, secret, match, line) {
			return true
		}
	}
	return false
}

// shannonEntropy is the formula gitleaks uses: the frequency of each rune over
// the byte length of the string. The terms are summed from the smallest count,
// not in map order, so that the float result is the same on every run (a value
// right at a rule's threshold must not flip between runs).
func shannonEntropy(s string) float64 {
	if s == "" {
		return 0
	}
	counts := map[rune]int{}
	for _, r := range s {
		counts[r]++
	}
	ns := make([]int, 0, len(counts))
	for _, n := range counts {
		ns = append(ns, n)
	}
	slices.Sort(ns)
	inv := 1.0 / float64(len(s))
	var e float64
	for _, n := range ns {
		f := float64(n) * inv
		e -= f * math.Log2(f)
	}
	return e
}
