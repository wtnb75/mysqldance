package writer

import (
	"fmt"
	"regexp"
	"strings"

	"mysqldance/internal/schemafile"
	"mysqldance/internal/sqlvalue"
)

// The view conversion for SQLite is an allow list: the body of SHOW CREATE VIEW
// is split into tokens, and only the keywords, operators and functions below are
// carried over. Anything else makes the view "not converted", which the writer
// reports with the reason; a view is never output half converted.

// sqliteViewKeywords are the words of a MySQL SELECT that mean the same in
// SQLite. A word that is not here (and is not a function below) is refused.
var sqliteViewKeywords = setOf("select", "distinct", "from", "where", "group", "by", "order", "having",
	"limit", "offset", "as", "join", "inner", "left", "outer", "cross", "on", "using", "and", "or", "not",
	"is", "null", "in", "like", "between", "case", "when", "then", "else", "end", "asc", "desc", "union",
	"all", "exists", "true", "false")

// sqliteViewFuncs are the functions SQLite has with the MySQL meaning.
var sqliteViewFuncs = setOf("count", "sum", "min", "max", "avg", "coalesce", "ifnull", "nullif", "abs",
	"round", "upper", "lower", "substr", "trim", "replace")

// sqliteViewOps are the operators that are carried over as they are. "/" and
// "%" are carried over too, but make the view cautioned.
var sqliteViewOps = setOf("(", ")", ",", ".", "=", "<", ">", "<=", ">=", "<>", "!=", "+", "-", "*")

// cautionOrder is the order the reasons are reported in, so that the warning is
// the same on every run.
var cautionOrder = []string{"a string literal", "a division", "a remainder (%)", "LIKE", "GROUP BY",
	"ORDER BY", "DISTINCT", "UNION", "AVG()", "SUM()", "ROUND()", "UPPER()", "LOWER()", "SUBSTR()"}

func setOf(ss ...string) map[string]bool {
	m := make(map[string]bool, len(ss))
	for _, s := range ss {
		m[s] = true
	}
	return m
}

const (
	tokSpace = iota
	tokIdent
	tokString
	tokNumber
	tokWord
	tokOp
)

type viewTok struct {
	kind int
	text string // the source text; the decoded value for tokString
}

// writeSQLiteViews writes the views that convert, in dependency order, and
// reports each one that is not output or whose result may differ. A view that
// reads a view that is not output is not output either: SQLite would accept it
// and fail only when it is queried.
func writeSQLiteViews(out *strings.Builder, sc Schema, warnf func(format string, a ...any)) {
	byName := make(map[string]int, len(sc.Views))
	names := make([]string, len(sc.Views))
	for i, v := range sc.Views {
		byName[v.Name] = i
		names[i] = v.Name
	}
	skipped := map[string]bool{}
	for _, n := range schemafile.OrderViews(names, sc.ViewDeps) {
		v := sc.Views[byName[n]]
		name := sqlvalue.QuoteIdent(v.Name)
		notOutput := func(reason string) {
			skipped[v.Name] = true
			warnf("view %s is not output: %s", name, reason)
		}
		if len(v.Reads) > 0 {
			qs := make([]string, len(v.Reads))
			for i, db := range v.Reads {
				qs[i] = sqlvalue.QuoteIdent(db)
			}
			notOutput("it reads another database (" + strings.Join(qs, ", ") + ")")
			continue
		}
		var dep string
		for _, d := range sc.ViewDeps[v.Name] {
			if skipped[d] {
				dep = d
				break
			}
		}
		if dep != "" {
			notOutput("it reads the view " + sqlvalue.QuoteIdent(dep) + ", which is not output")
			continue
		}
		stmt, caution, reason := convertSQLiteView(v.Name, v.Create)
		if reason != "" {
			notOutput(reason)
			continue
		}
		out.WriteString(stmt)
		if len(caution) > 0 {
			warnf("view %s is output, but its result may differ from MySQL: %s "+
				"(SQLite compares and sorts text byte by byte, and divides integers as integers)",
				name, strings.Join(caution, ", "))
		}
	}
}

// viewHeader is what SHOW CREATE VIEW writes before the SELECT, once the
// DEFINER clause is gone. The view name is quoted in backticks.
var viewHeader = regexp.MustCompile("^CREATE (?:OR REPLACE )?(?:ALGORITHM=\\w+ )?(?:SQL SECURITY \\w+ )?VIEW `(?:[^`]|``)*` AS ")

var viewCheckOption = regexp.MustCompile(`(?i)\s+WITH\s+(?:CASCADED\s+|LOCAL\s+)?CHECK\s+OPTION\s*$`)

// convertSQLiteView turns a MySQL SHOW CREATE VIEW statement of the view name
// into a SQLite CREATE VIEW statement ending in ";\n". If it cannot, reason says
// why and the statement is empty. caution lists what in the converted view may
// give a result that differs from MySQL.
func convertSQLiteView(name, create string) (stmt string, caution []string, reason string) {
	// The DEFINER clause holds quoted user names that may contain spaces.
	create = schemafile.StripDefiner(create)
	head := viewHeader.FindString(create)
	if head == "" {
		return "", nil, "its definition is not a CREATE VIEW statement this tool understands"
	}
	body := strings.TrimSpace(create[len(head):])
	if viewCheckOption.MatchString(body) {
		return "", nil, "WITH CHECK OPTION cannot be converted to SQLite"
	}
	toks, reason := lexView(body)
	if reason != "" {
		return "", nil, reason
	}
	c := &viewConv{caution: map[string]bool{}}
	out := c.convert(toks)
	if c.reason != "" {
		return "", nil, c.reason
	}
	for _, k := range cautionOrder {
		if c.caution[k] {
			caution = append(caution, k)
		}
	}
	return "CREATE VIEW " + sqlvalue.QuoteIdent(name) + " AS " + strings.TrimSpace(out) + ";\n", caution, ""
}

// lexView splits the body of a view into tokens. A character that has no place
// in a SELECT that SHOW CREATE VIEW prints (a comment, a variable, a hex
// literal) is refused.
func lexView(s string) ([]viewTok, string) {
	var toks []viewTok
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			j := i
			for j < len(s) && (s[j] == ' ' || s[j] == '\t' || s[j] == '\n' || s[j] == '\r') {
				j++
			}
			toks = append(toks, viewTok{tokSpace, s[i:j]})
			i = j
		case c == '`':
			j := i + 1
			for j < len(s) {
				if s[j] == '`' {
					if j+1 < len(s) && s[j+1] == '`' {
						j += 2
						continue
					}
					break
				}
				j++
			}
			if j >= len(s) {
				return nil, "an identifier is unterminated"
			}
			toks = append(toks, viewTok{tokIdent, s[i : j+1]})
			i = j + 1
		case c == '\'' || c == '"':
			val, end, ok := decodeMySQLString(s, i)
			if !ok {
				return nil, "a string literal is unterminated"
			}
			toks = append(toks, viewTok{tokString, val})
			i = end
		case c >= '0' && c <= '9' || c == '.' && i+1 < len(s) && s[i+1] >= '0' && s[i+1] <= '9':
			j := i
			for j < len(s) && (s[j] >= '0' && s[j] <= '9' || s[j] == '.') {
				j++
			}
			if j < len(s) && (s[j] == 'e' || s[j] == 'E') {
				k := j + 1
				if k < len(s) && (s[k] == '+' || s[k] == '-') {
					k++
				}
				if k < len(s) && s[k] >= '0' && s[k] <= '9' {
					for k < len(s) && s[k] >= '0' && s[k] <= '9' {
						k++
					}
					j = k
				}
			}
			toks = append(toks, viewTok{tokNumber, s[i:j]})
			i = j
		case isWordByte(c):
			j := i
			for j < len(s) && isWordByte(s[j]) {
				j++
			}
			word := s[i:j]
			// A character set introducer (_utf8mb4'x') is dropped; _binary changes
			// the meaning of the literal and is refused.
			if word[0] == '_' && j < len(s) && s[j] == '\'' {
				if strings.EqualFold(word, "_binary") {
					return nil, "_binary cannot be converted to SQLite"
				}
				i = j
				continue
			}
			toks = append(toks, viewTok{tokWord, word})
			i = j
		default:
			op := string(c)
			for _, m := range []string{"<=>", "<=", ">=", "<>", "!=", "||", "&&", "<<", ">>", ":=", "/*"} {
				if strings.HasPrefix(s[i:], m) {
					op = m
					break
				}
			}
			toks = append(toks, viewTok{tokOp, op})
			i += len(op)
		}
	}
	return toks, ""
}

func isWordByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_' || c == '$' || c >= '0' && c <= '9' || c >= 0x80
}

// decodeMySQLString decodes the string literal that starts at s[i] (a quote),
// with MySQL's backslash escapes and doubled quotes. end is the index after the
// closing quote.
func decodeMySQLString(s string, i int) (val string, end int, ok bool) {
	q := s[i]
	var b strings.Builder
	for j := i + 1; j < len(s); j++ {
		switch c := s[j]; {
		case c == '\\' && j+1 < len(s):
			j++
			switch e := s[j]; e {
			case 'n':
				b.WriteByte('\n')
			case 'r':
				b.WriteByte('\r')
			case 't':
				b.WriteByte('\t')
			case '0':
				b.WriteByte(0)
			case 'b':
				b.WriteByte('\b')
			case 'Z':
				b.WriteByte(26)
			case '%', '_':
				// Kept with the backslash: it is the LIKE escape, not a character.
				b.WriteByte('\\')
				b.WriteByte(e)
			default:
				b.WriteByte(e)
			}
		case c == q:
			if j+1 < len(s) && s[j+1] == q {
				b.WriteByte(q)
				j++
				continue
			}
			return b.String(), j + 1, true
		default:
			b.WriteByte(c)
		}
	}
	return "", 0, false
}

// viewConv converts tokens to SQLite text. reason is set on the first token
// that cannot be carried over; after that, convert returns early.
type viewConv struct {
	caution map[string]bool
	reason  string
	prev    string // the last word or operator that was converted, lower case
}

func (c *viewConv) fail(format string, a ...any) {
	if c.reason == "" {
		c.reason = fmt.Sprintf(format, a...) + " cannot be converted to SQLite"
	}
}

// nextSignificant returns the index of the first token at or after i that is not
// white space, or len(toks).
func nextSignificant(toks []viewTok, i int) int {
	for i < len(toks) && toks[i].kind == tokSpace {
		i++
	}
	return i
}

func (c *viewConv) convert(toks []viewTok) string {
	var out strings.Builder
	for i := 0; i < len(toks) && c.reason == ""; i++ {
		t := toks[i]
		switch t.kind {
		case tokSpace, tokIdent, tokNumber:
			out.WriteString(t.text)
		case tokString:
			c.caution["a string literal"] = true
			out.WriteString(sqlvalue.SQLiteLiteral("VARCHAR", []byte(t.text), false))
			c.prev = "'"
		case tokOp:
			switch {
			case t.text == "/":
				c.caution["a division"] = true
			case t.text == "%":
				c.caution["a remainder (%)"] = true
			case !sqliteViewOps[t.text]:
				c.fail("the operator %s", t.text)
				return ""
			}
			// "--" starts a comment in SQLite; MySQL's "a- -1" must not become one.
			if t.text == "-" && strings.HasSuffix(out.String(), "-") {
				out.WriteByte(' ')
			}
			out.WriteString(t.text)
			c.prev = t.text
		case tokWord:
			w := strings.ToLower(t.text)
			if i+1 < len(toks) && toks[i+1].kind == tokOp && toks[i+1].text == "(" && !sqliteViewKeywords[w] {
				end := matchingParen(toks, i+1)
				if end < 0 {
					c.fail("a parenthesis that is not closed")
					return ""
				}
				out.WriteString(c.call(w, toks[i+2:end]))
				i = end
				c.prev = ")"
				continue
			}
			if !sqliteViewKeywords[w] {
				c.fail("%s", strings.ToUpper(t.text))
				return ""
			}
			c.keyword(w, toks, i)
			out.WriteString(t.text)
			c.prev = w
		}
	}
	return out.String()
}

// keyword checks the context of a keyword that is only the same in SQLite there.
func (c *viewConv) keyword(w string, toks []viewTok, i int) {
	switch w {
	case "left", "inner", "cross":
		// A function named LEFT is not the join.
		if j := nextSignificant(toks, i+1); j >= len(toks) || toks[j].kind != tokWord ||
			!strings.EqualFold(toks[j].text, "join") && !strings.EqualFold(toks[j].text, "outer") {
			c.fail("%s", strings.ToUpper(w))
		}
	case "all":
		// ALL after UNION only; "> ALL (subquery)" is not SQLite.
		if c.prev != "union" {
			c.fail("ALL")
		}
	case "like":
		c.caution["LIKE"] = true
	case "distinct":
		c.caution["DISTINCT"] = true
	case "union":
		c.caution["UNION"] = true
	case "group", "order":
		if j := nextSignificant(toks, i+1); j < len(toks) && strings.EqualFold(toks[j].text, "by") {
			c.caution[strings.ToUpper(w)+" BY"] = true
		}
	}
}

// matchingParen returns the index of the parenthesis that closes the one at
// toks[open], or -1.
func matchingParen(toks []viewTok, open int) int {
	depth := 0
	for i := open; i < len(toks); i++ {
		if toks[i].kind != tokOp {
			continue
		}
		switch toks[i].text {
		case "(":
			depth++
		case ")":
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// call converts the function w with the arguments inside its parentheses.
func (c *viewConv) call(w string, inner []viewTok) string {
	switch w {
	case "if":
		args := c.args(inner, false)
		if c.reason != "" {
			return ""
		}
		if len(args) != 3 {
			c.fail("IF() with %d arguments", len(args))
			return ""
		}
		return "CASE WHEN " + args[0] + " THEN " + args[1] + " ELSE " + args[2] + " END"
	case "concat":
		args := c.args(inner, true)
		if c.reason != "" {
			return ""
		}
		return "(" + strings.Join(args, " || ") + ")"
	}
	if !sqliteViewFuncs[w] {
		c.fail("%s()", strings.ToUpper(w))
		return ""
	}
	switch w {
	case "count":
		// SQLite takes one expression in COUNT(DISTINCT ...) and reports the
		// error only when the view is queried.
		if len(splitArgs(inner)) > 1 {
			c.fail("COUNT(DISTINCT ...) with several arguments")
			return ""
		}
	case "avg", "sum", "round", "upper", "lower", "substr":
		c.caution[strings.ToUpper(w)+"()"] = true
	}
	return w + "(" + c.convert(inner) + ")"
}

// splitArgs splits the tokens inside the parentheses of a call at the commas
// outside any parenthesis.
func splitArgs(inner []viewTok) [][]viewTok {
	var out [][]viewTok
	depth, start := 0, 0
	for i := 0; i <= len(inner); i++ {
		if i < len(inner) && inner[i].kind == tokOp {
			switch inner[i].text {
			case "(":
				depth++
			case ")":
				depth--
			}
		}
		if i == len(inner) || depth == 0 && inner[i].kind == tokOp && inner[i].text == "," {
			out = append(out, inner[start:i])
			start = i + 1
		}
	}
	return out
}

// args converts the arguments of a call, each without the white space around
// it. With group set, an argument of more than one token is parenthesized, so
// that an operator it holds keeps its precedence next to the operator the
// caller joins the arguments with.
func (c *viewConv) args(inner []viewTok, group bool) []string {
	var out []string
	for _, a := range splitArgs(inner) {
		s := strings.TrimSpace(c.convert(a))
		if group && !isAtom(a) {
			s = "(" + s + ")"
		}
		out = append(out, s)
	}
	return out
}

// isAtom reports whether toks is one operand that no operator around it can
// split: a single token, a dotted name, a call, or a parenthesized expression.
func isAtom(toks []viewTok) bool {
	var sig []viewTok
	for _, t := range toks {
		if t.kind != tokSpace {
			sig = append(sig, t)
		}
	}
	if len(sig) <= 1 {
		return true
	}
	dotted := true
	for i, t := range sig {
		if i%2 == 0 && t.kind != tokIdent || i%2 == 1 && (t.kind != tokOp || t.text != ".") {
			dotted = false
		}
	}
	if dotted && len(sig)%2 == 1 {
		return true
	}
	open := 0
	if sig[0].kind == tokWord {
		open = 1
	}
	return sig[open].kind == tokOp && sig[open].text == "(" && matchingParen(sig, open) == len(sig)-1
}
