package writer

import (
	"slices"
	"strings"

	"mysqldance/internal/source"
)

// The cautions below need the type and the collation of the columns a view
// reads, which the SQL text of the view does not tell. They are found on the
// tokens of the view as MySQL prints it (SHOW CREATE VIEW), where a column is
// always written `alias`.`column` and a table is written `table` `alias` after
// FROM or JOIN. A column that cannot be resolved to a table of the same
// database (a column of a derived table or of a view, an unknown name, an alias
// that two tables share) is never a reason for a caution; the cautions of the
// syntax alone (cautionOrder) still apply to the view.

const (
	cautionNumericText = "a numeric column held as TEXT (compared or calculated as text)"
	cautionCaseText    = "a case-insensitive text column (SQLite compares it case-sensitively)"
)

var (
	viewCmpOps   = setOf("=", "<", ">", "<=", ">=", "<>", "!=")
	viewArithOps = setOf("+", "-", "*", "/", "%")
	// The aggregate and scalar functions whose argument a numeric column held as
	// TEXT changes the result of.
	viewNumericFuncs = setOf("sum", "avg", "min", "max", "round", "abs")
	viewTextTypes    = setOf("char", "varchar", "tinytext", "text", "mediumtext", "longtext", "enum", "set")
)

// sigToks are the tokens of a view without the white space. The tests take an
// index that may be out of range (the neighbour of the first or last token).
type sigToks []viewTok

func (s sigToks) at(i int) (viewTok, bool) {
	if i < 0 || i >= len(s) {
		return viewTok{}, false
	}
	return s[i], true
}

// op reports whether s[i] is one of the operators texts.
func (s sigToks) op(i int, texts ...string) bool {
	t, ok := s.at(i)
	return ok && t.kind == tokOp && slices.Contains(texts, t.text)
}

// opIn reports whether s[i] is an operator of set.
func (s sigToks) opIn(i int, set map[string]bool) bool {
	t, ok := s.at(i)
	return ok && t.kind == tokOp && set[t.text]
}

// word reports whether s[i] is the word w, in any case.
func (s sigToks) word(i int, w string) bool {
	t, ok := s.at(i)
	return ok && t.kind == tokWord && strings.EqualFold(t.text, w)
}

func (s sigToks) ident(i int) bool {
	t, ok := s.at(i)
	return ok && t.kind == tokIdent
}

// viewRef is a column of a table of the same database that a view reads, at
// sig[start] (the alias) to sig[end] (the column name).
type viewRef struct {
	start, end int
	col        source.ColumnDef
}

// typeCautions returns the cautions that follow from the types and collations
// of the columns the view reads. defs is Schema.Defs.
func typeCautions(toks []viewTok, defs map[string]source.TableDef) []string {
	var sig sigToks
	for _, t := range toks {
		if t.kind != tokSpace {
			sig = append(sig, t)
		}
	}
	refs := resolveViewColumns(sig, defs)
	byStart := make(map[int]viewRef, len(refs))
	byEnd := make(map[int]viewRef, len(refs))
	for _, r := range refs {
		byStart[r.start], byEnd[r.end] = r, r
	}
	var numeric, caseText bool
	for _, r := range refs {
		prev, next := r.start-1, r.end+1
		if isNumericText(r.col) {
			fn, isArg := argOf(sig, r)
			between := sig.word(next, "between") || sig.word(next, "not") && sig.word(next+1, "between") ||
				sig.word(prev, "between") || sig.word(prev, "and") && isBetweenAnd(sig, prev)
			if sig.opIn(prev, viewCmpOps) || sig.opIn(prev, viewArithOps) ||
				sig.opIn(next, viewCmpOps) || sig.opIn(next, viewArithOps) ||
				between || isArg && viewNumericFuncs[fn] {
				numeric = true
			}
		}
		if isCaseInsensitiveText(r.col) {
			fn, isArg := argOf(sig, r)
			// The other side of the comparison has to be a column of text too: a
			// literal is the caution of "a string literal". Its collation is not
			// looked at (the decided spec of B-113; review ledger R-224).
			other := false
			if sig.opIn(prev, viewCmpOps) {
				o, ok := byEnd[prev-1]
				other = ok && isTextColumn(o.col)
			}
			if sig.opIn(next, viewCmpOps) {
				o, ok := byStart[next+1]
				other = other || ok && isTextColumn(o.col)
			}
			if other || isArg && (fn == "min" || fn == "max") {
				caseText = true
			}
		}
	}
	var out []string
	if numeric {
		out = append(out, cautionNumericText)
	}
	if caseText {
		out = append(out, cautionCaseText)
	}
	return out
}

// resolveViewColumns finds the `alias`.`column` names in sig that name a column
// of a table in defs.
func resolveViewColumns(sig sigToks, defs map[string]source.TableDef) []viewRef {
	// An alias that two tables (or a table and a derived table) share anywhere in
	// the view is not resolved at all: the scope is not tracked, and not resolving
	// is the decided way to be wrong (B-113; review ledger R-225).
	// alias -> table, from "FROM|JOIN [(...] `table` [AS] [`alias`]".
	aliases := map[string]string{}
	ambiguous := map[string]bool{}
	for i := range sig {
		if !sig.word(i, "from") && !sig.word(i, "join") {
			continue
		}
		j := i + 1
		for sig.op(j, "(") {
			j++
		}
		// A derived table starts with SELECT: its alias is not a table to resolve,
		// and a base table of the same name in another scope must not be read for
		// it.
		if sig.word(j, "select") && j > i+1 {
			if end := matchingParen(sig, j-1); end >= 0 {
				if a, ok := aliasAt(sig, end+1); ok {
					ambiguous[a] = true
				}
			}
			continue
		}
		if !sig.ident(j) {
			continue
		}
		// `db`.`table` is another database or a qualified name: not a table to
		// resolve either, and neither is its alias.
		if sig.op(j+1, ".") {
			if a, ok := aliasAt(sig, j+3); ok {
				ambiguous[a] = true
			}
			continue
		}
		table := unquoteIdent(sig[j].text)
		alias := table
		if a, ok := aliasAt(sig, j+1); ok {
			alias = a
		}
		if prev, ok := aliases[alias]; ok && prev != table {
			ambiguous[alias] = true
		}
		aliases[alias] = table
	}

	var refs []viewRef
	for i := 0; i+2 < len(sig); i++ {
		if !sig.ident(i) || !sig.op(i+1, ".") || !sig.ident(i+2) || sig.op(i-1, ".") || sig.op(i+3, ".") {
			continue
		}
		alias := unquoteIdent(sig[i].text)
		table, ok := aliases[alias]
		if !ok || ambiguous[alias] {
			continue
		}
		name := unquoteIdent(sig[i+2].text)
		def, ok := tableDef(defs, table)
		if !ok {
			continue
		}
		if x := slices.IndexFunc(def.Columns, func(c source.ColumnDef) bool { return strings.EqualFold(c.Name, name) }); x >= 0 {
			refs = append(refs, viewRef{start: i, end: i + 2, col: def.Columns[x]})
		}
	}
	return refs
}

// aliasAt returns the alias written at sig[k], after an optional AS.
func aliasAt(sig sigToks, k int) (string, bool) {
	if sig.word(k, "as") {
		k++
	}
	if !sig.ident(k) {
		return "", false
	}
	return unquoteIdent(sig[k].text), true
}

// tableDef finds the table a view names. The name is the one in the definition
// of the view, which keeps its case where lower_case_table_names=2 (and a
// server with lower_case_table_names=1 lists the table in lower case), so a
// name that matches only without regard to case counts, if it is the only one.
func tableDef(defs map[string]source.TableDef, name string) (source.TableDef, bool) {
	if d, ok := defs[name]; ok {
		return d, true
	}
	var found source.TableDef
	n := 0
	for k, d := range defs {
		if strings.EqualFold(k, name) {
			found, n = d, n+1
		}
	}
	return found, n == 1
}

// isNumericText reports whether c is a number that SQLite holds as TEXT:
// DECIMAL, BIGINT UNSIGNED and BIT of 64 bits.
func isNumericText(c source.ColumnDef) bool {
	switch c.DataType {
	case "decimal", "numeric", "bigint", "bit":
		typ, err := sqliteType(c)
		return err == nil && typ == "TEXT"
	}
	return false
}

func isTextColumn(c source.ColumnDef) bool { return viewTextTypes[c.DataType] }

// isCaseInsensitiveText reports whether c is text that MySQL compares without
// regard to case.
func isCaseInsensitiveText(c source.ColumnDef) bool {
	return isTextColumn(c) && strings.HasSuffix(strings.ToLower(c.Collation), "_ci")
}

// argOf returns the lower-case name of the function that the column r is a
// whole argument of, as in sum(`t`.`c`) or round(`t`.`c`,2). Any position counts,
// so round(1,`t`.`c`) is cautioned too (the decided spec of B-113; review
// ledger R-227). The caller uses the name only if it is one of a few function
// names.
func argOf(sig sigToks, r viewRef) (fn string, ok bool) {
	prev, next := r.start-1, r.end+1
	if !sig.op(prev, "(", ",") && !sig.word(prev, "distinct") || !sig.op(next, ")", ",") {
		return "", false
	}
	// The parenthesis that is open at the column.
	depth := 0
	for j := r.start - 1; j >= 0; j-- {
		switch {
		case sig.op(j, ")"):
			depth++
		case sig.op(j, "("):
			if depth > 0 {
				depth--
				continue
			}
			if j > 0 && sig[j-1].kind == tokWord {
				return strings.ToLower(sig[j-1].text), true
			}
			return "", false
		}
	}
	return "", false
}

// isBetweenAnd reports whether the AND at sig[k] is the one of a BETWEEN, not a
// boolean operator.
func isBetweenAnd(sig sigToks, k int) bool {
	depth := 0
	for j := k - 1; j >= 0; j-- {
		t := sig[j]
		if t.kind == tokOp {
			switch t.text {
			case ")":
				depth++
			case "(":
				if depth == 0 {
					return false
				}
				depth--
			}
			continue
		}
		if depth > 0 || t.kind != tokWord {
			continue
		}
		switch strings.ToLower(t.text) {
		case "between":
			return true
		case "and", "or", "where", "on", "when", "then", "else", "having", "select":
			return false
		}
	}
	return false
}
