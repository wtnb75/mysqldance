package naming

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// MaxFileNameBytes is the common file-name length limit of file systems.
const MaxFileNameBytes = 255

const (
	// shortMark separates the kept head of a shortened name from its hash.
	// EscapeName escapes '~', so no unshortened name contains it and a
	// shortened name never equals an unshortened one.
	shortMark = "~"
	// hashLen is the number of hex digits of the hash of a shortened name.
	hashLen = 16
	// minShortLen is the least length a name can be shortened to: one kept
	// byte, the mark and the hash.
	minShortLen = 1 + len(shortMark) + hashLen
	// dbShortLen is the length a long database name is shortened to. It leaves
	// 155 bytes for the rest of a data file name.
	dbShortLen = 100
)

// shorten cuts the escaped name esc to at most maxLen bytes: a head of the
// name (never ending inside a %XX) plus shortMark and a fixed-length hash of
// the whole name. A name that already fits is returned as it is, as is one
// that cannot be shortened to maxLen.
func shorten(esc string, maxLen int) string {
	if len(esc) <= maxLen || maxLen < minShortLen {
		return esc
	}
	keep := maxLen - len(shortMark) - hashLen
	// Do not cut a %XX in the middle.
	if esc[keep-1] == '%' {
		keep--
	} else if keep >= 2 && esc[keep-2] == '%' {
		keep -= 2
	}
	sum := sha256.Sum256([]byte(esc))
	return esc[:keep] + shortMark + hex.EncodeToString(sum[:])[:hashLen]
}

// Plan holds the file names of one database. A name that would be over
// MaxFileNameBytes is shortened, and only then. The database part is the same
// in every file of the database (so the files still sort together, schema
// first), and is decided by the database and its tables alone.
type Plan struct {
	db     string
	dbEsc  string // EscapeName(db)
	dbComp string // the database part used in file names
}

// NewPlan returns the plan for db with the given tables. The database part is
// shortened when some file of the database is over the limit and the database
// part of that file is not shorter than its table part.
func NewPlan(db string, tables []string) *Plan {
	p := &Plan{db: db, dbEsc: EscapeName(db)}
	p.dbComp = p.dbEsc
	over := len(p.dbEsc)+len(constraintsSuffix) > MaxFileNameBytes
	for _, t := range tables {
		et := EscapeName(t)
		if len(p.dbEsc)+len(dataInfix)+len(et)+len(sqlExt) <= MaxFileNameBytes {
			continue
		}
		// The table part is shortened instead, unless it is the shorter one or
		// there is too little room left to shorten it into.
		room := MaxFileNameBytes - len(p.dbEsc) - len(dataInfix) - len(sqlExt)
		if len(p.dbEsc) >= len(et) || room < minShortLen {
			over = true
		}
	}
	if over {
		p.dbComp = shorten(p.dbEsc, dbShortLen)
	}
	return p
}

// DBShortened reports whether the database part of the file names is shortened.
func (p *Plan) DBShortened() bool { return p.dbComp != p.dbEsc }

// DBComponent returns the database part of the file names.
func (p *Plan) DBComponent() string { return p.dbComp }

// TableComponent returns the table part of the data file names of a table
// written as n files.
func (p *Plan) TableComponent(table string, n int) string {
	rest := len(p.dbComp) + len(dataInfix) + len(sqlExt) + partLen(n)
	return shorten(EscapeName(table), MaxFileNameBytes-rest)
}

// TableShortened reports whether the table part of the data file names of a
// table written as n files is shortened.
func (p *Plan) TableShortened(table string, n int) bool {
	return p.TableComponent(table, n) != EscapeName(table)
}

// partLen is the length of the part number (with its dot) in the names of a
// table written as n files; 0 for a single file.
func partLen(n int) int {
	if n <= 1 {
		return 0
	}
	return 1 + max(minPartDigits, len(fmt.Sprint(n)))
}

// SchemaFile returns the schema file name of the database.
func (p *Plan) SchemaFile() string { return p.dbComp + schemaSuffix }

// RoutinesFile returns the name of the file that holds the routines of the
// database (see RoutinesFile).
func (p *Plan) RoutinesFile() string { return p.dbComp + routinesSuffix }

// TriggersFile returns the name of the file that holds the triggers of the
// database (see TriggersFile).
func (p *Plan) TriggersFile() string { return p.dbComp + triggersSuffix }

// ConstraintsFile returns the name of the constraints file of the database (see
// ConstraintsFile).
func (p *Plan) ConstraintsFile() string { return p.dbComp + constraintsSuffix }

// DataFile returns the data file name of a table.
func (p *Plan) DataFile(table string) string { return p.DataFileParts(table, 1)[0] }

// DataFileParts returns the data file names of a table written as n files (see
// DataFileParts).
func (p *Plan) DataFileParts(table string, n int) []string {
	return dataFileParts(p.dbComp, p.TableComponent(table, n), n)
}

// IsOwned reports whether file is the schema file, the routines file, the triggers file, the
// constraints file or a data file of the database. Both forms of the database
// part count, shortened or not, whatever this run uses: whether it is
// shortened depends on the tables, so the files of an earlier dump may have the
// other form (the tables have changed since) and must not be left behind.
func (p *Plan) IsOwned(file string) bool {
	return isOwned(file, p.dbEsc) || isOwned(file, shorten(p.dbEsc, dbShortLen))
}

func isOwned(file, dbComp string) bool {
	if file == dbComp+schemaSuffix || file == dbComp+routinesSuffix || file == dbComp+triggersSuffix || file == dbComp+constraintsSuffix {
		return true
	}
	return strings.HasPrefix(file, dbComp+dataInfix) && strings.HasSuffix(file, sqlExt)
}
