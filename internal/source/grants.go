package source

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/go-sql-driver/mysql"

	"mysqldance/internal/sqlvalue"
)

// grant is one privilege list of SHOW GRANTS, for the whole server (db "*") or
// for the databases a pattern names. Grants on a table, a column or a routine
// are not kept: they do not let the account see every object of a kind.
type grant struct {
	db    string
	privs []string
}

// grantSet is what SHOW GRANTS FOR CURRENT_USER() says the account holds.
type grantSet []grant

// parseGrants reads the rows of SHOW GRANTS FOR CURRENT_USER(). ok is false when
// a row cannot be read or no row names a scope, so the caller cannot tell what
// the account holds; it must not then claim that a privilege is missing. The
// privilege names are all that is kept, not the rest of a row: the row can hold
// a password hash (see the design document, section 11).
func parseGrants(lines []string) (set grantSet, ok bool) {
	for _, line := range lines {
		rest, found := strings.CutPrefix(line, "GRANT ")
		if !found {
			return nil, false
		}
		// A role granted to the account has no ON. Whether SHOW GRANTS already
		// adds what the role holds depends on the server, so the privileges cannot
		// be told from the rows.
		onAt := indexOutsideQuotes(rest, " ON ")
		if onAt < 0 {
			return nil, false
		}
		privs := splitPrivileges(rest[:onAt])
		if len(privs) == 1 && privs[0] == "PROXY" {
			continue
		}
		scope, found := parseScope(rest[onAt+len(" ON "):])
		if !found {
			return nil, false
		}
		switch {
		case scope.wide:
			set = append(set, grant{db: scope.db, privs: privs})
			// SELECT on *.* or on mysql.* includes SELECT on mysql.proc, whichever
			// database is dumped.
			if scope.db == "*" || matchDBPattern(scope.db, "mysql") {
				set = append(set, grant{db: "*", privs: procTablePrivileges(privs)})
			}
		case scope.procTable:
			set = append(set, grant{db: "*", privs: procTablePrivileges(privs)})
		}
		ok = true
	}
	return set, ok
}

// scope is the object a grant is on.
type scope struct {
	db string
	// wide is true for *.* and for db.*, where the privilege covers every object
	// of a kind in the database.
	wide bool
	// procTable is true for the table mysql.proc, which MySQL 5.7 and MariaDB
	// keep their routines in.
	procTable bool
}

// allPrivileges is how every server prints ALL in SHOW GRANTS.
const allPrivileges = "ALL PRIVILEGES"

// selectOnProcTable stands for SELECT on mysql.proc in a grantSet. On MySQL 5.7
// and MariaDB it lists every routine, and lets the account read their bodies
// (EXECUTE and ALTER ROUTINE list them but return no statement).
const selectOnProcTable = "SELECT ON MYSQL.PROC"

// procTablePrivileges returns selectOnProcTable when privs holds SELECT or ALL,
// which is all that matters of a grant on mysql.proc.
func procTablePrivileges(privs []string) []string {
	for _, p := range privs {
		if p == "SELECT" || p == allPrivileges {
			return []string{selectOnProcTable}
		}
	}
	return nil
}

// parseScope reads what follows ON in a GRANT row, up to TO.
func parseScope(s string) (scope, bool) {
	toAt := indexOutsideQuotes(s, " TO ")
	if toAt < 0 {
		return scope{}, false
	}
	s = s[:toAt]
	// An object kind in front: TABLE, FUNCTION or PROCEDURE.
	for _, kind := range []string{"TABLE ", "FUNCTION ", "PROCEDURE "} {
		if rest, found := strings.CutPrefix(s, kind); found {
			if _, _, ok := splitQualified(rest); !ok {
				return scope{}, false
			}
			return scope{}, true
		}
	}
	db, obj, ok := splitQualified(s)
	if !ok {
		return scope{}, false
	}
	return scope{db: db, wide: obj == "*", procTable: db == "mysql" && obj == "proc"}, true
}

// splitQualified splits db.object; each part is `quoted` (a doubled backtick is
// one) or bare, and the object may be *. The db keeps its backslashes, which
// escape the wildcards of a pattern (see matchDBPattern).
func splitQualified(s string) (db, obj string, ok bool) {
	db, rest, ok := readIdent(s)
	if !ok || !strings.HasPrefix(rest, ".") {
		return "", "", false
	}
	obj, rest, ok = readIdent(rest[1:])
	if !ok || rest != "" {
		return "", "", false
	}
	return db, obj, true
}

// readIdent reads one identifier or * from the front of s.
func readIdent(s string) (ident, rest string, ok bool) {
	if s == "" {
		return "", "", false
	}
	if s[0] == '`' {
		var b strings.Builder
		for i := 1; i < len(s); i++ {
			if s[i] != '`' {
				b.WriteByte(s[i])
				continue
			}
			if i+1 < len(s) && s[i+1] == '`' {
				b.WriteByte('`')
				i++
				continue
			}
			return b.String(), s[i+1:], true
		}
		return "", "", false
	}
	end := strings.IndexByte(s, '.')
	if end < 0 {
		end = len(s)
	}
	if end == 0 || strings.ContainsAny(s[:end], " `'\"?") {
		return "", "", false
	}
	// Cloned, as in splitPrivileges: the row can hold a password hash.
	return strings.Clone(s[:end]), s[end:], true
}

// indexOutsideQuotes returns the index of the first sep in s that is not inside
// a `quoted` or 'quoted' part, or -1.
func indexOutsideQuotes(s, sep string) int {
	var quote byte
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '`' || c == '\'':
			quote = c
		case strings.HasPrefix(s[i:], sep):
			return i
		}
	}
	return -1
}

// splitPrivileges returns the upper-cased names of a privilege list. A column
// list (SELECT (`a`, `b`)) is dropped: those grants are on a table anyway.
func splitPrivileges(s string) []string {
	var out []string
	depth, start := 0, 0
	flush := func(end int) {
		name := strings.TrimSpace(s[start:end])
		if i := strings.IndexByte(name, '('); i >= 0 {
			name = strings.TrimSpace(name[:i])
		}
		if name != "" {
			// Cloned: ToUpper returns a name that is already upper case as it is,
			// which would keep the whole row (and its password hash) alive.
			out = append(out, strings.Clone(strings.ToUpper(name)))
		}
	}
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				flush(i)
				start = i + 1
			}
		}
	}
	flush(len(s))
	return out
}

// covers reports whether the account holds one of privs on every object of its
// kind in db, through a grant on *.* or on a database pattern that matches db.
// ALL PRIVILEGES covers every name. A grant on one table or routine is not kept
// (see grant), so an account that holds only those is refused: they do not show
// every object, and what is hidden cannot be seen (review ledger R-218).
func (g grantSet) covers(db string, privs ...string) bool {
	for _, gr := range g {
		if gr.db != "*" && !matchDBPattern(gr.db, db) {
			continue
		}
		for _, have := range gr.privs {
			if have == allPrivileges {
				return true
			}
			if slices.Contains(privs, have) {
				return true
			}
		}
	}
	return false
}

// matchDBPattern reports whether name matches a database name of a grant: _ is
// any one character and % any run, and a backslash makes either one literal.
// The comparison is case-sensitive on purpose: folding the case would pass a
// grant on `MyDB` for `mydb` where lower_case_table_names=0, and the check would
// then let hidden objects drop silently (review ledger R-217).
func matchDBPattern(pattern, name string) bool {
	if pattern == "" {
		return name == ""
	}
	switch pattern[0] {
	case '%':
		for i := 0; i <= len(name); i++ {
			if matchDBPattern(pattern[1:], name[i:]) {
				return true
			}
		}
		return false
	case '_':
		return name != "" && matchDBPattern(pattern[1:], name[1:])
	case '\\':
		if len(pattern) > 1 {
			pattern = pattern[1:]
		}
	}
	return name != "" && name[0] == pattern[0] && matchDBPattern(pattern[1:], name[1:])
}

// requirePrivilege fails when the account is known not to hold one of privs on
// every object of a kind in the current database. The server lists only what
// the account may see (information_schema.TRIGGERS, ROUTINES and EVENTS give no
// error and no row for the rest), and nothing else reveals that a hidden object
// exists, so a privilege that is missing is an error even when there may be no
// object: leaving the objects out of the dump would be silent. kind is the
// plural name of the objects, for the message.
//
// When the account's privileges cannot be read (SHOW GRANTS fails or has a
// form this does not know, or the account has a role), the check is skipped
// rather than failed: a privilege is claimed missing only when it is known to be.
func (s *Snapshot) requirePrivilege(ctx context.Context, kind string, privs ...string) error {
	grants, ok, err := s.readGrants(ctx)
	if err != nil {
		return err
	}
	if !ok || grants.covers(s.name, privs...) {
		return nil
	}
	return fmt.Errorf("the account may not list the %s of database %s: it needs one of %s, granted on *.* or on the database;"+
		" without it the server leaves them out of its catalog, so they would be missing from the dump",
		kind, sqlvalue.QuoteIdent(s.name), strings.Join(privs, ", "))
}

// readGrants reads the grants of the account once. ok is false when the server
// refused the statement or its rows are not understood (or the account has a
// role: SHOW GRANTS then has a `GRANT role TO account` row, on MySQL 8.x and
// MariaDB alike); a warning then says, once, that the privilege check is
// skipped, so the skip is not silent. An error that is not the server's answer
// (a broken connection, a cancel, a timeout) is returned: the read after it
// would fail too, and it must not turn the check off.
func (s *Snapshot) readGrants(ctx context.Context) (grants grantSet, ok bool, err error) {
	if s.grantsRead {
		return s.grants, s.grantsOK, nil
	}
	lines, err := s.queryGrants(ctx)
	if err != nil {
		var serverErr *mysql.MySQLError
		if !errors.As(err, &serverErr) {
			return nil, false, err
		}
		s.grantsRead = true
		s.warnGrantsSkipped("SHOW GRANTS failed", "error", err)
		return nil, false, nil
	}
	s.grantsRead = true
	s.grants, s.grantsOK = parseGrants(lines)
	if !s.grantsOK {
		s.warnGrantsSkipped("SHOW GRANTS has a form this does not understand, or the account has a role")
	}
	return s.grants, s.grantsOK, nil
}

// warnGrantsSkipped logs why the privilege check did not run. A Snapshot made
// without Open has no logger.
func (s *Snapshot) warnGrantsSkipped(reason string, args ...any) {
	if s.log == nil {
		return
	}
	s.log.Warn("the privileges to list triggers, routines and events are not checked: "+reason, args...)
}

// queryGrants returns the rows of SHOW GRANTS FOR CURRENT_USER().
func (s *Snapshot) queryGrants(ctx context.Context) ([]string, error) {
	rows, err := s.conn.QueryContext(ctx, "SHOW GRANTS FOR CURRENT_USER()")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var lines []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			return nil, err
		}
		lines = append(lines, line)
	}
	return lines, rows.Err()
}
