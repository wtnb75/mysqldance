package schemafile

import (
	"fmt"
	"io"
	"strings"

	"mysqldance/internal/datafile"
	"mysqldance/internal/sqlvalue"
)

// Trigger is a trigger with its CREATE TRIGGER statement (without a trailing
// semicolon) and the sql_mode it was created under. Table is the table it is
// on; it is not written, only reported.
type Trigger struct{ Name, Table, Create, SQLMode string }

// WriteTriggers writes the triggers file: for each trigger, in the given
// order, a DROP, the SET SQL_MODE of its creation (the mode changes how the
// body is parsed), and the CREATE. A body may hold ';', so the statements end
// with a custom delimiter set by the mysql client's DELIMITER command, which is
// what /docker-entrypoint-initdb.d runs the files with.
func WriteTriggers(w io.Writer, db string, triggers []Trigger) error {
	// The creation charset and collation are not kept on purpose: the file runs
	// under the header's SET NAMES utf8mb4, so the output does not depend on the
	// connection that created the trigger (review ledger R-087).
	stmts := make([][]string, len(triggers))
	for i, t := range triggers {
		if hasDelimiterLine(t.Create) {
			return fmt.Errorf("trigger %s has a line that starts with DELIMITER, which the mysql client would run as a command",
				sqlvalue.QuoteIdent(t.Name))
		}
		stmts[i] = dropSetCreate("TRIGGER", t.Name, t.SQLMode, t.Create)
	}
	return writeDelimited(w, db, stmts)
}

// StripTriggerDB removes the database qualifier from the name of the trigger
// and from the table it is on in a CREATE TRIGGER statement (DEFINER already
// removed), so the heading restores under another database name. MariaDB and
// MySQL 5.7 return the statement as it was written, qualifiers included (checked
// by TestTriggersCreatedWithDatabaseNameRestoreUnderAnotherName); what 8.x
// returns for a qualified trigger was not examined, and unqualified input
// passes through unchanged. Only the heading up to FOR EACH ROW is read; the
// body is not changed, so a database name inside it stays (B-154).
// A trigger lives in the database of its table, so a qualifier other than db is
// an error (compared case-insensitively: the server never creates a trigger in
// another schema, so this only guards against output we cannot place), and so is a heading that does not read as
// CREATE TRIGGER name {BEFORE|AFTER} {INSERT|UPDATE|DELETE} ON table FOR EACH ROW
// (IF NOT EXISTS, OR REPLACE, an odd qualifier): nothing is passed on unread.
func StripTriggerDB(create, db string) (string, error) {
	h := headScanner{s: create}
	var cuts [][2]int // [start, end) of each qualifier, with its dot
	qualified := func() bool {
		start, end, ok := h.ident()
		if !ok {
			return false
		}
		if h.peek() != '.' {
			return true
		}
		h.i++
		nameStart, _, ok := h.ident()
		if !ok || h.peek() == '.' {
			return false
		}
		if !strings.EqualFold(unquoteIdent(create[start:end]), db) {
			h.err = fmt.Errorf("trigger heading names database %s, not %s: %q",
				create[start:end], sqlvalue.QuoteIdent(db), create)
			return false
		}
		cuts = append(cuts, [2]int{start, nameStart})
		return true
	}
	ok := h.word("CREATE") && h.word("TRIGGER") && qualified() &&
		(h.word("BEFORE") || h.word("AFTER")) &&
		(h.word("INSERT") || h.word("UPDATE") || h.word("DELETE")) &&
		h.word("ON") && qualified() &&
		h.word("FOR") && h.word("EACH") && h.word("ROW")
	if h.err != nil {
		return "", h.err
	}
	if !ok {
		return "", fmt.Errorf("cannot parse trigger heading: %q", create)
	}
	var b strings.Builder
	prev := 0
	for _, c := range cuts {
		b.WriteString(create[prev:c[0]])
		prev = c[1]
	}
	b.WriteString(create[prev:])
	return b.String(), nil
}

// headScanner reads the keywords and names of a trigger heading.
type headScanner struct {
	s   string
	i   int
	err error
}

func (h *headScanner) skipSpace() {
	for h.i < len(h.s) && strings.IndexByte(" \t\r\n", h.s[h.i]) >= 0 {
		h.i++
	}
}

func (h *headScanner) peek() byte {
	if h.i < len(h.s) {
		return h.s[h.i]
	}
	return 0
}

func isBareIdentByte(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_' || c == '$' || c >= 0x80
}

// word consumes the keyword w (any case), which must not run into a longer word.
func (h *headScanner) word(w string) bool {
	h.skipSpace()
	end := h.i + len(w)
	if end > len(h.s) || !strings.EqualFold(h.s[h.i:end], w) || (end < len(h.s) && isBareIdentByte(h.s[end])) {
		return false
	}
	h.i = end
	return true
}

// ident consumes one identifier, quoted with backticks or bare, and returns its
// byte range in s (quotes included).
func (h *headScanner) ident() (start, end int, ok bool) {
	h.skipSpace()
	start = h.i
	// A double-quoted name is an identifier under ANSI_QUOTES, which the
	// trigger may have been created under (the statement is kept as written).
	if h.peek() == '`' || h.peek() == '"' {
		closing := skipQuoted(h.s, h.i)
		if closing < 0 {
			return 0, 0, false
		}
		h.i = closing + 1
		return start, h.i, true
	}
	for h.i < len(h.s) && isBareIdentByte(h.s[h.i]) {
		h.i++
	}
	return start, h.i, h.i > start
}

// unquoteIdent returns the name of an identifier as ident read it.
func unquoteIdent(id string) string {
	if len(id) >= 2 && (id[0] == '`' || id[0] == '"') {
		q := string(id[0])
		return strings.ReplaceAll(id[1:len(id)-1], q+q, q)
	}
	return id
}

// dropSetCreate returns the three statements that re-create one object: the
// DROP, the SET SQL_MODE of its creation, and the CREATE.
func dropSetCreate(kind, name, sqlMode, create string) []string {
	return []string{
		"DROP " + kind + " IF EXISTS " + sqlvalue.QuoteIdent(name),
		"SET SQL_MODE=" + sqlvalue.Literal("VARCHAR", []byte(sqlMode), false),
		create,
	}
}

// writeDelimited writes the header, then each group of statements (a blank line
// before each group), every statement ended by one delimiter that none of them
// contains.
func writeDelimited(w io.Writer, db string, groups [][]string) error {
	var flat []string
	for _, ss := range groups {
		flat = append(flat, ss...)
	}
	delim := chooseDelimiter(flat)

	var b strings.Builder
	b.WriteString(datafile.Header(db))
	b.WriteString("DELIMITER " + delim + "\n")
	for _, ss := range groups {
		b.WriteString("\n")
		for _, s := range ss {
			fmt.Fprintf(&b, "%s%s\n", s, delim)
		}
	}
	b.WriteString("\nDELIMITER ;\n")
	_, err := io.WriteString(w, b.String())
	return err
}

// hasDelimiterLine reports whether a line of s starts with the client command
// DELIMITER (in any case), which the client would run even inside a statement.
func hasDelimiterLine(s string) bool {
	const word = "DELIMITER"
	for line := range strings.SplitSeq(s, "\n") {
		line = strings.TrimLeft(line, " \t")
		if len(line) >= len(word) && strings.EqualFold(line[:len(word)], word) {
			return true
		}
	}
	return false
}

// chooseDelimiter returns the first of "$$", "//", "##", "%%", "!!", "$$$", ...
// that ends every statement unambiguously. The client cuts at the first
// occurrence, so the delimiter must not appear inside a statement, nor be
// formed together with the statement's last characters (`SET @a$` followed by
// `$$` reads as `SET @a` and a stray `$`). It depends on the statements alone,
// so the output stays deterministic.
func chooseDelimiter(stmts []string) string {
	for n := 2; ; n++ {
		for _, c := range "$/#%!" {
			d := strings.Repeat(string(c), n)
			ok := true
			for _, s := range stmts {
				if strings.Index(s+d, d) != len(s) {
					ok = false
					break
				}
			}
			if ok {
				return d
			}
		}
	}
}
