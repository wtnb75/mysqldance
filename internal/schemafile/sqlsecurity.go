package schemafile

import (
	"errors"
	"regexp"
	"strings"
)

// Anchored to the statement header, so a view's name and body are never touched.
var viewSecurity = regexp.MustCompile("^(CREATE (?:OR REPLACE )?(?:ALGORITHM=\\w+ )?(?:DEFINER=" + ident + "@" + ident + " )?)SQL SECURITY (DEFINER|INVOKER)( VIEW )")

// invokerLine is the line SHOW CREATE writes for a routine with SQL SECURITY
// INVOKER, after the signature line.
const invokerLine = "    SQL SECURITY INVOKER"

// ViewSecurityInvoker replaces SQL SECURITY DEFINER in the header of a SHOW
// CREATE VIEW statement with SQL SECURITY INVOKER (--sql-security=invoker). A
// view always has one of the two words there. The rest of the statement is not
// touched. A header that does not have the expected form is an error: the view
// would otherwise keep running with its definer's privileges.
func ViewSecurityInvoker(createView string) (string, error) {
	if !viewSecurity.MatchString(createView) {
		return "", errors.New("cannot find SQL SECURITY in the view header")
	}
	return viewSecurity.ReplaceAllString(createView, "${1}SQL SECURITY INVOKER${3}"), nil
}

// RoutineSecurityInvoker makes a SHOW CREATE PROCEDURE|FUNCTION statement run
// as SQL SECURITY INVOKER (--sql-security=invoker). SHOW CREATE writes no SQL
// SECURITY for the default DEFINER, and a line "    SQL SECURITY INVOKER"
// after the signature line for INVOKER, so the line is added after the
// signature line unless it is already there. The characteristics are
// indented lines and the body starts at the first line that is not, as SHOW
// CREATE writes them.
//
// A statement without the end of the signature line is an error: the routine
// would otherwise keep running with its definer's privileges.
func RoutineSecurityInvoker(createRoutine string) (string, error) {
	end := signatureEnd(createRoutine)
	if end < 0 {
		return "", errors.New("cannot find the end of the routine signature line")
	}
	// The characteristic lines: the indented lines after the signature line.
	// An explicit DEFINER line is replaced, so SQL SECURITY is not given twice.
	pos := end + 1
	for pos < len(createRoutine) && (createRoutine[pos] == ' ' || createRoutine[pos] == '\t') {
		// A quoted comment can hold newlines, so the line ends at a newline
		// outside quotes, not at the next one.
		lineEnd := newlineOutsideQuotes(createRoutine[pos:])
		if lineEnd < 0 {
			lineEnd = len(createRoutine) - pos
		}
		switch strings.TrimSpace(createRoutine[pos : pos+lineEnd]) {
		case "SQL SECURITY INVOKER":
			return createRoutine, nil
		case "SQL SECURITY DEFINER":
			return createRoutine[:pos] + invokerLine + createRoutine[pos+lineEnd:], nil
		}
		pos += lineEnd + 1
	}
	return createRoutine[:end] + "\n" + invokerLine + createRoutine[end:], nil
}

// signatureEnd returns the index of the newline that ends the signature line of
// a CREATE PROCEDURE|FUNCTION statement: the first newline outside quotes,
// backticks and parentheses (an ENUM type or a quoted name can hold one). It
// returns -1 when there is none or a quote is not closed.
func signatureEnd(s string) int { return newlineOutsideQuotes(s) }

// newlineOutsideQuotes returns the index of the first newline in s that is
// outside quotes, backticks and parentheses. It returns -1 when there is none
// or a quote is not closed.
func newlineOutsideQuotes(s string) int {
	depth := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\'', '"', '`':
			end := skipQuoted(s, i)
			if end < 0 {
				return -1
			}
			i = end
		case '(':
			depth++
		case ')':
			if depth > 0 {
				depth--
			}
		case '\n':
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}
