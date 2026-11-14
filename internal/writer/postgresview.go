package writer

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"mysqldance/internal/source"
	"mysqldance/internal/sqlvalue"
)

// PostgreSQL checks a view when it is created: a type that does not fit an
// operator, or a column that is selected without being grouped, is an error, and
// the error stops the restore. So besides the allow list that SQLite uses, the
// shapes that are certain to be refused are found here and the view is not
// output. What cannot be found without the types of the expressions is left to
// the PostgreSQL integration tests.

// pgViewEnv is what the checks need to know about the tables of the database.
type pgViewEnv struct {
	// BoolCols holds the names of the columns that become boolean (TINYINT(1)),
	// in any table. A name is enough: a view's column names are not resolved to
	// tables, so a boolean column and an integer column of the same name in two
	// tables are both treated as boolean (the view is refused, never wrong).
	BoolCols map[string]bool
	// PKs maps a table to the columns of its primary key.
	PKs map[string][]string
	// NotOut holds the names of the columns that are not output (generated
	// columns), in any table, by name for the same reason as BoolCols. A view
	// that reads one fails when PostgreSQL creates it.
	NotOut map[string]bool
}

func newPGViewEnv(defs map[string]source.TableDef) pgViewEnv {
	env := pgViewEnv{BoolCols: map[string]bool{}, PKs: map[string][]string{}, NotOut: map[string]bool{}}
	for table, def := range defs {
		for _, c := range def.Columns {
			if c.Generated {
				env.NotOut[c.Name] = true
			}
			if isPGBoolean(c) {
				env.BoolCols[c.Name] = true
			}
		}
		for _, ix := range def.Indexes {
			if ix.Name != "PRIMARY" {
				continue
			}
			cols := make([]string, 0, len(ix.Parts))
			for _, p := range ix.Parts {
				cols = append(cols, p.Column)
			}
			env.PKs[table] = cols
		}
	}
	return env
}

// pgViewFuncs are the functions PostgreSQL has with the MySQL meaning. ROUND is
// not one of them: PostgreSQL rounds only numeric (not double precision) to a
// number of digits, and the type of a column is not known here.
var pgViewFuncs = setOf("count", "sum", "min", "max", "avg", "coalesce", "ifnull", "nullif", "abs",
	"upper", "lower", "substr", "trim", "replace")

var pgDialect = &viewDialect{
	target: "PostgreSQL",
	funcs:  pgViewFuncs,
	ident: func(tok string) (string, string) {
		name := unquoteIdent(tok)
		if len(name) > pgMaxIdent {
			return "", fmt.Sprintf("the name %s is %d bytes, over PostgreSQL's %d-byte limit for names",
				sqlvalue.QuoteIdent(name), len(name), pgMaxIdent)
		}
		return sqlvalue.QuotePGIdent(name), ""
	},
	str: func(v string) (string, string) {
		lit, err := sqlvalue.PostgresLiteral(sqlvalue.PGColumn{Kind: sqlvalue.PGText}, []byte(v), false)
		if err != nil {
			return "", "a string literal cannot be converted to PostgreSQL: " + err.Error()
		}
		return lit, ""
	},
	note: "PostgreSQL compares and sorts text by its own collation, not MySQL's, puts NULL last in an ascending order, " +
		"and divides integers as integers",
	postgresql: true,
}

// convertPostgresView converts a view for PostgreSQL; see convertView.
func convertPostgresView(name, create string, env pgViewEnv) (stmt string, caution []string, reason string) {
	return convertView(pgDialect, &env, name, create)
}

// boolCallRefused are the functions whose arguments cannot be boolean in
// PostgreSQL: SUM(boolean) and the like do not exist, and CONCAT of a boolean
// gives true/false where MySQL gives 1/0.
var boolCallRefused = setOf("sum", "avg", "abs", "min", "max", "upper", "lower", "substr", "trim", "replace", "concat",
	"coalesce", "nullif")

var numericOps = setOf("+", "-", "*", "/", "%")
var compareOps = setOf("=", "<", ">", "<=", ">=", "<>", "!=")

// checkBool refuses the view when the identifier toks[i] is a boolean column
// used as a number: in arithmetic, compared with a number, or as the argument
// of a function that takes no boolean. A boolean column that is only selected,
// or used as a condition, passes. It returns false (with the reason set) when
// the view is refused.
func (c *viewConv) checkBool(toks []viewTok, i int) bool {
	name := unquoteIdent(toks[i].text)
	if !c.env.BoolCols[name] && !c.env.NotOut[name] {
		return true
	}
	// Only the column of a "table.column" reference counts: the table name and
	// an alias (after AS) can have the same name.
	p := prevSignificant(toks, i)
	if p < 0 || toks[p].kind != tokOp || toks[p].text != "." {
		return true
	}
	if c.env.NotOut[name] {
		// PostgreSQL checks the columns when the view is created.
		c.reason = fmt.Sprintf("it reads the column %s, which is a generated column and is not output", sqlvalue.QuoteIdent(name))
		return false
	}
	refuse := func(what string) bool {
		c.reason = fmt.Sprintf("the boolean column %s (TINYINT(1) becomes boolean) is used %s, which PostgreSQL does not allow",
			sqlvalue.QuoteIdent(name), what)
		return false
	}
	if n := len(c.calls); n > 0 && boolCallRefused[c.calls[n-1]] {
		return refuse("as an argument of " + strings.ToUpper(c.calls[n-1]) + "()")
	}
	// The operators on either side, and the operand on their far side.
	if q := prevSignificant(toks, p); q >= 0 {
		// toks[q] is the table name; the operator is before it.
		if o := prevSignificant(toks, q); o >= 0 && toks[o].kind == tokOp {
			if numericOps[toks[o].text] {
				return refuse("in arithmetic")
			}
			if compareOps[toks[o].text] {
				if f := prevSignificant(toks, o); f >= 0 && toks[f].kind == tokNumber {
					return refuse("in a comparison with a number")
				}
			}
		}
	}
	w := nextSignificant(toks, i+1)
	if w < len(toks) && isWord(toks[w], "not") {
		w = nextSignificant(toks, w+1)
	}
	if w < len(toks) && (isWord(toks[w], "in") || isWord(toks[w], "between")) {
		return refuse("in a list or range of numbers")
	}
	if o := nextSignificant(toks, i+1); o < len(toks) && toks[o].kind == tokOp {
		if numericOps[toks[o].text] {
			return refuse("in arithmetic")
		}
		if compareOps[toks[o].text] {
			if f := nextSignificant(toks, o+1); f < len(toks) && toks[f].kind == tokNumber {
				return refuse("in a comparison with a number")
			}
		}
	}
	return true
}

// unquoteIdent returns the name inside a backtick-quoted identifier token.
func unquoteIdent(tok string) string {
	return strings.ReplaceAll(tok[1:len(tok)-1], "``", "`")
}

// isStringLiteral reports whether toks is one string literal and white space.
func isStringLiteral(toks []viewTok) bool {
	n := 0
	for _, t := range toks {
		switch t.kind {
		case tokSpace:
		case tokString:
			n++
		default:
			return false
		}
	}
	return n == 1
}

// prevSignificant returns the index of the last token before i that is not
// white space, or -1.
func prevSignificant(toks []viewTok, i int) int {
	for i--; i >= 0; i-- {
		if toks[i].kind != tokSpace {
			return i
		}
	}
	return -1
}

// limit rewrites MySQL's "LIMIT offset,count" at toks[i] as "LIMIT count OFFSET
// offset", which is all PostgreSQL takes. It returns the index of the last token
// it used and the text; the text is empty when the clause needs no rewriting.
func (c *viewConv) limit(toks []viewTok, i int) (end int, out string) {
	a := nextSignificant(toks, i+1)
	if a >= len(toks) || toks[a].kind != tokNumber {
		c.fail("LIMIT with something other than numbers")
		return 0, ""
	}
	comma := nextSignificant(toks, a+1)
	if comma >= len(toks) || toks[comma].kind != tokOp || toks[comma].text != "," {
		return 0, ""
	}
	b := nextSignificant(toks, comma+1)
	if b >= len(toks) || toks[b].kind != tokNumber {
		c.fail("LIMIT with something other than numbers")
		return 0, ""
	}
	// MySQL's way to skip rows without a limit is a count of 2^64-1, which
	// PostgreSQL's bigint cannot hold.
	for _, n := range []string{toks[a].text, toks[b].text} {
		if _, err := strconv.ParseInt(n, 10, 64); err != nil {
			c.fail("LIMIT with the number %s", n)
			return 0, ""
		}
	}
	return b, toks[i].text + " " + toks[b].text + " offset " + toks[a].text
}

var aggregateFuncs = setOf("count", "sum", "min", "max", "avg")

// checkGroupBy refuses a view whose GROUP BY leaves a selected column
// ungrouped: MySQL (with ONLY_FULL_GROUP_BY) accepts a column that depends on
// the grouped columns, and so does PostgreSQL, but only through a primary key
// that is wholly grouped. An expression is ungrouped unless the same text is
// in the GROUP BY or it holds an aggregate. Only the query itself and each
// branch of a UNION are checked, not subqueries.
func checkGroupBy(toks []viewTok, env *pgViewEnv) string {
	var sig []viewTok
	for _, t := range toks {
		if t.kind != tokSpace {
			sig = append(sig, t)
		}
	}
	// Split into the branches of the UNION, at depth 0.
	start, depth := 0, 0
	for i := 0; i <= len(sig); i++ {
		if i < len(sig) {
			switch {
			case sig[i].kind == tokOp && sig[i].text == "(":
				depth++
			case sig[i].kind == tokOp && sig[i].text == ")":
				depth--
			}
		}
		if i == len(sig) || depth == 0 && isWord(sig[i], "union") {
			if reason := checkBranchGroupBy(sig[start:i], env); reason != "" {
				return reason
			}
			start = i + 1
		}
	}
	return ""
}

func isWord(t viewTok, w string) bool { return t.kind == tokWord && strings.EqualFold(t.text, w) }

// depth0 returns the indexes of the tokens of sig at depth 0 for which f is true.
func depth0(sig []viewTok, f func(i int) bool) []int {
	var out []int
	depth := 0
	for i, t := range sig {
		if t.kind == tokOp && t.text == ")" {
			depth--
		}
		if depth == 0 && f(i) {
			out = append(out, i)
		}
		if t.kind == tokOp && t.text == "(" {
			depth++
		}
	}
	return out
}

// splitTop splits sig at the commas of depth 0.
func splitTop(sig []viewTok) [][]viewTok {
	var out [][]viewTok
	prev := 0
	for _, i := range depth0(sig, func(i int) bool { return sig[i].kind == tokOp && sig[i].text == "," }) {
		out = append(out, sig[prev:i])
		prev = i + 1
	}
	return append(out, sig[prev:])
}

func joinToks(sig []viewTok) string {
	var b strings.Builder
	for _, t := range sig {
		if t.kind == tokString {
			b.WriteString("'" + t.text + "'")
			continue
		}
		b.WriteString(t.text)
	}
	return b.String()
}

func checkBranchGroupBy(sig []viewTok, env *pgViewEnv) string {
	groups := depth0(sig, func(i int) bool {
		return isWord(sig[i], "group") && i+1 < len(sig) && isWord(sig[i+1], "by")
	})
	froms := depth0(sig, func(i int) bool { return isWord(sig[i], "from") })
	if len(groups) == 0 || len(froms) == 0 || len(sig) == 0 || !isWord(sig[0], "select") {
		return ""
	}
	list := sig[1:froms[0]]
	if len(list) > 0 && isWord(list[0], "distinct") {
		list = list[1:]
	}
	rest := sig[groups[0]+2:]
	if ends := depth0(rest, func(i int) bool {
		return isWord(rest[i], "having") || isWord(rest[i], "order") || isWord(rest[i], "limit")
	}); len(ends) > 0 {
		rest = rest[:ends[0]]
	}
	// MySQL prints an aliased table as `table` `alias`. The qualifier of a column
	// is then not the table, and its primary key is not looked up.
	aliased := false
	for i := 0; i+1 < len(sig); i++ {
		if sig[i].kind == tokIdent && sig[i+1].kind == tokIdent {
			aliased = true
		}
	}
	grouped := map[string]bool{}
	for _, g := range splitTop(rest) {
		grouped[joinToks(g)] = true
	}
	for _, item := range splitTop(list) {
		if n := len(item); n >= 2 && isWord(item[n-2], "as") {
			item = item[:n-2]
		}
		if grouped[joinToks(item)] || hasAggregate(item) || hasSelect(item) || !hasColumn(item) {
			continue
		}
		if !aliased && len(item) == 3 && item[0].kind == tokIdent && item[1].text == "." && item[2].kind == tokIdent {
			table := unquoteIdent(item[0].text)
			if pk := env.PKs[table]; len(pk) > 0 && allGrouped(table, pk, grouped) {
				continue
			}
		}
		return fmt.Sprintf("it selects %s without grouping it (PostgreSQL accepts that only for the columns of a grouped primary key)",
			joinToks(item))
	}
	return ""
}

func allGrouped(table string, pk []string, grouped map[string]bool) bool {
	for _, col := range pk {
		if !grouped[sqlvalue.QuoteIdent(table)+"."+sqlvalue.QuoteIdent(col)] {
			return false
		}
	}
	return true
}

// hasColumn reports whether the item reads a column; a constant needs no grouping.
func hasColumn(item []viewTok) bool {
	return slices.ContainsFunc(item, func(t viewTok) bool { return t.kind == tokIdent })
}

// hasSelect reports whether the item holds a subquery, which is not checked.
func hasSelect(item []viewTok) bool {
	return slices.ContainsFunc(item, func(t viewTok) bool { return isWord(t, "select") })
}

func hasAggregate(item []viewTok) bool {
	for i, t := range item {
		if t.kind == tokWord && aggregateFuncs[strings.ToLower(t.text)] && i+1 < len(item) && item[i+1].text == "(" {
			return true
		}
	}
	return false
}
