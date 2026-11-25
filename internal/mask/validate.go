package mask

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
)

// CatalogColumn is a column of a table as the catalog lists it. TypeName is the
// driver's type name (sqlvalue.Column TypeName); ColumnType, DataType and
// CharacterSet are what information_schema reports (COLUMN_TYPE, DATA_TYPE,
// CHARACTER_SET_NAME). A Generated column is not output, so a rule on it could
// never apply.
type CatalogColumn struct {
	Name         string
	TypeName     string
	ColumnType   string
	DataType     string
	CharacterSet string
	// Collation is COLLATION_NAME, empty for a column that has none.
	Collation string
	Nullable  bool
	Generated bool
}

// CatalogIndex is an index of a table: its name and its columns in key order
// (a functional key part is left out). PrefixColumns are the columns of which
// only a prefix is indexed, and Functional is true when a key part is an
// expression: the dump cannot check such a unique index for collisions.
type CatalogIndex struct {
	Name          string
	Unique        bool
	Columns       []string
	PrefixColumns []string
	Functional    bool
}

// CatalogFK is a foreign key of a table: its columns, and the columns of the
// table (in RefDB) it references.
type CatalogFK struct {
	Name       string
	Columns    []string
	RefDB      string
	RefTable   string
	RefColumns []string
}

// CatalogTable is a table or view of a database being dumped. Excluded marks a
// table left out of the dump by --exclude-table. OrderBy is the key the rows
// are ordered by (empty when there is none).
type CatalogTable struct {
	Name        string
	View        bool
	Excluded    bool
	Columns     []CatalogColumn
	OrderBy     []string
	Indexes     []CatalogIndex
	ForeignKeys []CatalogFK
}

// Catalog is what the rules are checked against: the databases being dumped
// and their tables. A database that is not a key of Databases is not dumped
// (or does not exist). FoldTableCase is set when the server compares table
// names without regard to case (lower_case_table_names is not 0); column names
// are always compared without regard to case.
type Catalog struct {
	FoldTableCase bool
	Databases     map[string][]CatalogTable
}

func (c Catalog) sameTable(a, b string) bool {
	if c.FoldTableCase {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// database finds a database being dumped, by the catalog's spelling, which it
// also returns. Database names follow the same rule as table names.
func (c Catalog) database(name string) (string, []CatalogTable, bool) {
	if tables, ok := c.Databases[name]; ok {
		return name, tables, true
	}
	if c.FoldTableCase {
		for db, tables := range c.Databases {
			if strings.EqualFold(db, name) {
				return db, tables, true
			}
		}
	}
	return "", nil, false
}

// Wildcard is the column part of a key that stands for every column of a table
// that has no rule of its own (DB.TABLE.*). Only keep can be used on it, so a
// column named "*" cannot be given a rule.
const Wildcard = "*"

// lookupTable finds the base table a key names among the tables being dumped,
// and returns it with the catalog's spelling of its database. A table that does
// not exist, a view and a table left out by --exclude-table are errors, since
// none of them has rows in the output.
func (c Catalog) lookupTable(k Key) (string, *CatalogTable, error) {
	dbName, tables, ok := c.database(k.DB)
	if !ok {
		return "", nil, fmt.Errorf("database %q is not one of the databases to dump", k.DB)
	}
	for i := range tables {
		if !c.sameTable(tables[i].Name, k.Table) {
			continue
		}
		switch tbl := &tables[i]; {
		case tbl.View:
			return "", nil, fmt.Errorf("%q is a view, not a base table; its rows are not output", k.Table)
		case tbl.Excluded:
			return "", nil, fmt.Errorf("table %q is excluded from the dump", k.Table)
		default:
			return dbName, tbl, nil
		}
	}
	return "", nil, fmt.Errorf("no such table %q in database %q", k.Table, k.DB)
}

// normKey is the form of a column in which two spellings of it are equal; the
// arguments are the catalog's spelling.
func (c Catalog) normKey(db, table, column string) string {
	if c.FoldTableCase {
		table = strings.ToLower(table)
	}
	return db + "\x00" + table + "\x00" + strings.ToLower(column)
}

// CheckStrictColumns is the check behind --strict-columns: every column that is
// output (a column of a base table that --exclude-table leaves in, not a
// generated one) needs a decision, that is, a rule of its own with any
// transform (keep included), or a DB.TABLE.* rule for its table. The columns
// without one are all listed, as DB.TABLE.COLUMN and never with a value, in the
// order of the databases' names, then of the catalog. It expects rules that
// Resolve accepted; a rule that names nothing in the catalog decides nothing.
func (r *Rules) CheckStrictColumns(cat Catalog) error {
	decided := map[string]bool{}
	for _, e := range r.Entries {
		// A rule that has neither a transform nor paths decides nothing.
		if e.Rule.Use == "" && len(e.Rule.Paths) == 0 {
			continue
		}
		dbName, tbl, err := cat.lookupTable(e.Key)
		if err != nil {
			continue
		}
		decided[cat.normKey(dbName, tbl.Name, e.Key.Column)] = true
	}
	dbs := make([]string, 0, len(cat.Databases))
	for name := range cat.Databases {
		dbs = append(dbs, name)
	}
	slices.Sort(dbs)
	var undecided []string
	for _, db := range dbs {
		for _, tbl := range cat.Databases[db] {
			if tbl.View || tbl.Excluded || decided[cat.normKey(db, tbl.Name, Wildcard)] {
				continue
			}
			for _, col := range tbl.Columns {
				if !col.Generated && !decided[cat.normKey(db, tbl.Name, col.Name)] {
					undecided = append(undecided, db+"."+tbl.Name+"."+col.Name)
				}
			}
		}
	}
	if len(undecided) == 0 {
		return nil
	}
	return fmt.Errorf("--strict-columns: %d column(s) have no rule; give each one a rule, or \"use: keep\" if it needs no masking (DB.TABLE.%s covers every column of a table that has no rule of its own):\n  %s",
		len(undecided), Wildcard, strings.Join(undecided, "\n  "))
}

// Validate checks every rule against the catalog and the registry, before any
// output is written; see Resolve. It passes no salt, so a rule that uses a
// transform that needs one is an error.
func (r *Rules) Validate(cat Catalog, reg *Registry) error {
	_, err := r.Resolve(cat, reg, nil)
	return err
}

// Resolve checks every rule against the catalog and the registry, before any
// output is written, and prepares the transforms. It reports every problem
// found (joined, in file order): a database that is not dumped, a table that
// does not exist, is a view or is excluded, a column that does not exist or is
// generated, two rules for one column, a transform that does not apply to the
// type of its column, a key of the rule that this version does not support, a
// transform on a column of a key that it cannot be used on, a transform that
// needs a salt when salt is nil, and what the transform itself finds wrong
// with its arguments or its column.
func (r *Rules) Resolve(cat Catalog, reg *Registry, salt *Salt) (*Plan, error) {
	var errs []error
	plan := &Plan{tables: map[string][]plannedRule{}, checked: map[string]*checkedTable{}}
	seen := map[string]string{} // normalized key -> spec of the first rule
	byColumn := r.columnRules(cat)
	for _, e := range r.Entries {
		bad := func(format string, args ...any) {
			errs = append(errs, fmt.Errorf("rule %s (line %d): %s", e.Spec, e.Line, fmt.Sprintf(format, args...)))
		}
		if e.Key.Table == Wildcard {
			bad("the table part cannot be %q: only the column part may be (DB.TABLE.%s)", Wildcard, Wildcard)
			continue
		}
		dbName, tbl, err := cat.lookupTable(e.Key)
		if err != nil {
			bad("%v", err)
			continue
		}
		if e.Key.Column == Wildcard {
			// DB.TABLE.*: every column of the table that has no rule of its own is
			// kept. It changes nothing, so it plans nothing.
			if e.Rule.Quasi {
				bad("a wildcard rule (DB.TABLE.%s) cannot be marked quasi: name the columns", Wildcard)
				continue
			}
			if e.Rule.Use != keepTransform.Name {
				bad("only %q can be used on a wildcard (DB.TABLE.%s), not %q", keepTransform.Name, Wildcard, e.Rule.Use)
				continue
			}
			norm := cat.normKey(dbName, tbl.Name, Wildcard)
			if first, dup := seen[norm]; dup {
				bad("table %q already has a wildcard rule (%s)", tbl.Name, first)
				continue
			}
			seen[norm] = e.Spec
			if err := checkUnsupported(e.Rule); err != nil {
				bad("%v", err)
			}
			if e.Rule.Domain != "" {
				bad("a wildcard rule (DB.TABLE.%s) cannot have a domain: keep uses no key", Wildcard)
			}
			continue
		}
		var col *CatalogColumn
		for i := range tbl.Columns {
			if strings.EqualFold(tbl.Columns[i].Name, e.Key.Column) {
				col = &tbl.Columns[i]
				break
			}
		}
		switch {
		case col == nil:
			bad("no such column %q in table %q", e.Key.Column, tbl.Name)
			continue
		case col.Generated:
			bad("column %q of table %q is generated, so it is not output", col.Name, tbl.Name)
			continue
		}
		// The catalog's spelling names the column, so two spellings of one
		// column meet here.
		norm := cat.normKey(dbName, tbl.Name, col.Name)
		if first, dup := seen[norm]; dup {
			bad("column %q of table %q already has a rule (%s)", col.Name, tbl.Name, first)
			continue
		}
		seen[norm] = e.Spec
		if err := checkUnsupported(e.Rule); err != nil {
			bad("%v", err)
		}
		if err := checkDomainUse(e.Rule, reg); err != nil {
			bad("%v", err)
			continue
		}
		if e.Rule.Use == "" && len(e.Rule.Paths) == 0 {
			// Only quasi (the parser refuses a rule with nothing at all): the column
			// is marked for scan-k, and a dump does nothing for it.
			continue
		}
		if err := checkApplies(e, *col, reg); err != nil {
			bad("%v", err)
			continue
		}
		if len(e.Rule.Paths) > 0 {
			fn, leaves, err := buildJSONPaths(e, *col, reg, salt)
			if err != nil {
				bad("%v", err)
				continue
			}
			plan.add(dbName, tbl.Name, columnOf(tableColumns(tbl), col.Name), e.Spec, "", fn, leaves)
			continue
		}
		t, _ := reg.Lookup(e.Rule.Use)
		if t.NoChange {
			// Nothing is transformed, so no key can break.
			continue
		}
		if err := cat.checkKeys(dbName, *tbl, *col, t, e.Rule, byColumn); err != nil {
			bad("%v", err)
			continue
		}
		cols := tableColumns(tbl)
		c := columnOf(cols, col.Name)
		fn, err := buildRule(e, t, c, cols, salt)
		if err != nil {
			bad("%v", err)
			continue
		}
		plan.add(dbName, tbl.Name, c, e.Spec, t.Name, fn, nil)
		if t.UniqueChecked {
			plan.noteChecked(dbName, *tbl, col.Name)
		}
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	plan.buildUniques()
	return plan, nil
}

// checkUnsupported reports the keys of a rule that the parser reads and checks
// but that have no meaning yet. They must not be dropped silently: the rule
// would then mask less than its author wrote.
func checkUnsupported(r Rule) error {
	var keys []string
	for _, k := range []struct{ name, val string }{
		{"when", r.When}, {"on_null", r.OnNull}, {"on_collision", r.OnCollision},
	} {
		if k.val != "" {
			keys = append(keys, k.name)
		}
	}
	if len(keys) == 0 {
		return nil
	}
	return fmt.Errorf("%s is not supported in this version", strings.Join(keys, ", "))
}

// checkApplies checks the transform of a rule against the type of the column.
// A rule given per JSON path needs a JSON column; the type of what a path
// reaches is not known here, so the transforms below a path are checked for
// their names and arguments only (when the file is parsed).
func checkApplies(e Entry, col CatalogColumn, reg *Registry) error {
	if len(e.Rule.Paths) > 0 {
		if !strings.EqualFold(col.TypeName, "JSON") {
			return fmt.Errorf("paths can only be given for a JSON column, but %q is %s", col.Name, col.TypeName)
		}
		return nil
	}
	t, ok := reg.Lookup(e.Rule.Use)
	if !ok {
		return fmt.Errorf("unknown transform %q", e.Rule.Use)
	}
	if !t.appliesTo(col.TypeName) {
		return fmt.Errorf("transform %q does not apply to column %q of type %s (applies to: %s)",
			t.Name, col.Name, col.TypeName, strings.Join(t.Types, ", "))
	}
	return nil
}

// columnRules maps every column that a rule of this file names (not a wildcard)
// to the rule, by the catalog's spelling of the column (Catalog.normKey). The
// foreign key checks look the other side of a key up in it.
func (r *Rules) columnRules(cat Catalog) map[string]Rule {
	out := map[string]Rule{}
	for _, e := range r.Entries {
		if e.Key.Table == Wildcard || e.Key.Column == Wildcard {
			continue
		}
		dbName, tbl, err := cat.lookupTable(e.Key)
		if err != nil {
			continue
		}
		for _, c := range tbl.Columns {
			if strings.EqualFold(c.Name, e.Key.Column) {
				if _, dup := out[cat.normKey(dbName, tbl.Name, c.Name)]; !dup {
					out[cat.normKey(dbName, tbl.Name, c.Name)] = e.Rule
				}
				break
			}
		}
	}
	return out
}

// checkKeys refuses a transform on a column that other rows or tables depend on.
//
// A column that another table references (the parent side of a foreign key) can
// be transformed only if every child column of those keys has the same rule
// (same transform, arguments and domain, so the same value gives the same
// output), and the column is not the child side of a key itself. A transform
// that BreaksKeys is also refused on the ordering key and on the columns of a
// unique index, and on the child side of a foreign key unless the parent column
// has the same rule. byColumn holds the rules of the file. The message names the
// kind of key, never a value. "The same rule" gives the same output for the same
// bytes only; the server matches a key under the collation of the column, which
// this does not look at (B-222).
func (c Catalog) checkKeys(db string, tbl CatalogTable, col CatalogColumn, t Transform, rule Rule, byColumn map[string]Rule) error {
	refs := c.referencingKeys(db, tbl, col.Name)
	for _, ref := range refs {
		child := ref.fk.Columns[ref.pos]
		other, has := byColumn[c.normKey(ref.db, ref.table.Name, child)]
		if !sameRule(rule, other, has) {
			return fmt.Errorf("column %q of table %q is referenced by a foreign key of table %s.%s (%q), so it can be transformed only if its child column %q has the same transform, arguments and domain (its child rows would no longer find it)",
				col.Name, tbl.Name, ref.db, ref.table.Name, ref.fk.Name, child)
		}
	}
	if len(refs) > 0 {
		if name := fkOf(tbl, col.Name); name != "" {
			return fmt.Errorf("column %q of table %q is referenced by a foreign key and is also part of the foreign key %q, so it cannot be transformed", col.Name, tbl.Name, name)
		}
	}
	if !t.BreaksKeys {
		return nil
	}
	if containsFold(tbl.OrderBy, col.Name) {
		return keyError(t, col, tbl, "part of the key the rows are ordered by")
	}
	if k := uniqueKind(tbl, col.Name, t.UniqueChecked); k != "" {
		return keyError(t, col, tbl, k)
	}
	for _, fk := range tbl.ForeignKeys {
		pos := slices.IndexFunc(fk.Columns, func(x string) bool { return strings.EqualFold(x, col.Name) })
		if pos < 0 {
			continue
		}
		if pos >= len(fk.RefColumns) {
			return fmt.Errorf("foreign key %q of table %q has %d columns but %d referenced columns, so it cannot be checked", fk.Name, tbl.Name, len(fk.Columns), len(fk.RefColumns))
		}
		parentDB, parentTable, parentCol := "", "", fk.RefColumns[pos]
		if name, tables, ok := c.database(fk.RefDB); ok {
			for _, pt := range tables {
				if c.sameTable(pt.Name, fk.RefTable) && !pt.View && !pt.Excluded {
					parentDB, parentTable = name, pt.Name
					break
				}
			}
		}
		if parentTable == "" {
			return keyError(t, col, tbl, fmt.Sprintf("part of the foreign key %q, whose parent table %s.%s is not in the dump", fk.Name, fk.RefDB, fk.RefTable))
		}
		other, has := byColumn[c.normKey(parentDB, parentTable, parentCol)]
		if !sameRule(rule, other, has) {
			return keyError(t, col, tbl, fmt.Sprintf("part of the foreign key %q, and the column it references (%s.%s.%s) has not the same transform, arguments and domain", fk.Name, fk.RefDB, fk.RefTable, parentCol))
		}
	}
	return nil
}

func keyError(t Transform, col CatalogColumn, tbl CatalogTable, kind string) error {
	return fmt.Errorf("transform %q cannot be used on column %q of table %q: it is %s, and the transform would give rows the same value or break a reference",
		t.Name, col.Name, tbl.Name, kind)
}

// fkRef is a foreign key that references a column: the table it belongs to (in
// database db) and the position of the column among the key's referenced columns.
type fkRef struct {
	db    string
	table CatalogTable
	fk    CatalogFK
	pos   int
}

// referencingKeys returns the foreign keys that reference the column. Tables left
// out of the dump are not looked at. Databases are visited in name order, so the
// result does not depend on the order of a map.
func (c Catalog) referencingKeys(db string, tbl CatalogTable, col string) []fkRef {
	dbs := make([]string, 0, len(c.Databases))
	for name := range c.Databases {
		dbs = append(dbs, name)
	}
	slices.Sort(dbs)
	var out []fkRef
	for _, name := range dbs {
		for _, other := range c.Databases[name] {
			if other.View || other.Excluded {
				continue
			}
			for _, fk := range other.ForeignKeys {
				if !c.sameTable(fk.RefDB, db) || !c.sameTable(fk.RefTable, tbl.Name) {
					continue
				}
				// The catalog builds Columns and RefColumns together, so their lengths are
				// equal; the bound only guards a hand-made catalog (R-236).
				if pos := slices.IndexFunc(fk.RefColumns, func(x string) bool { return strings.EqualFold(x, col) }); pos >= 0 && pos < len(fk.Columns) {
					out = append(out, fkRef{db: name, table: other, fk: fk, pos: pos})
				}
			}
		}
	}
	return out
}

// uniqueKind says why the unique indexes of the table forbid a transform that
// breaks keys on the column, or returns "" if none does. The primary key always
// forbids it. Another unique index forbids it unless the transform is
// UniqueChecked and the dump can compare the rows on the index: it cannot when
// only a prefix of the column is indexed (the prefix may collide when the whole
// value does not), or when a part of the index is an expression or a generated
// column (the rows do not hold its value).
func uniqueKind(tbl CatalogTable, col string, checked bool) string {
	for _, ix := range tbl.Indexes {
		if !ix.Unique || !containsFold(ix.Columns, col) {
			continue
		}
		switch {
		case ix.Name == "PRIMARY":
			return "part of the primary key"
		case !checked:
			return fmt.Sprintf("part of the unique index %q", ix.Name)
		case len(ix.PrefixColumns) > 0:
			// Of this column or of another one of the index: the server compares only
			// the prefix, the dump the whole value.
			return fmt.Sprintf("part of the unique index %q, which indexes a prefix of a column, so a collision cannot be detected", ix.Name)
		case ix.Functional:
			return fmt.Sprintf("part of the unique index %q, which has an expression, so a collision cannot be detected", ix.Name)
		case hasGeneratedColumn(tbl, ix):
			return fmt.Sprintf("part of the unique index %q, which has a generated column, so a collision cannot be detected", ix.Name)
		}
	}
	return ""
}

func hasGeneratedColumn(tbl CatalogTable, ix CatalogIndex) bool {
	for _, c := range tbl.Columns {
		if c.Generated && containsFold(ix.Columns, c.Name) {
			return true
		}
	}
	return false
}

// fkOf returns the name of the first foreign key of the table that has the
// column on its child side, or "".
func fkOf(tbl CatalogTable, col string) string {
	for _, fk := range tbl.ForeignKeys {
		if containsFold(fk.Columns, col) {
			return fk.Name
		}
	}
	return ""
}

func containsFold(list []string, s string) bool {
	return slices.ContainsFunc(list, func(x string) bool { return strings.EqualFold(x, s) })
}

// keyOf derives the HMAC key of a transform that needs a salt, and returns nil
// for one that does not. A rule with a domain gets the key of that domain.
func keyOf(t Transform, salt *Salt, domain string) ([]byte, error) {
	if !t.NeedsSalt {
		return nil, nil
	}
	if salt == nil {
		return nil, fmt.Errorf("transform %q needs a salt: pass --salt-file or set %s (there is no default salt)", t.Name, SaltEnv)
	}
	return DeriveDomainKey(*salt, t.Name, t.Version, domain), nil
}

// checkDomainName checks the format of a domain: 1 to 64 characters out of the
// ASCII letters, digits, "_" and "-". The case counts.
func checkDomainName(s string) error {
	if s == "" || len(s) > 64 {
		return fmt.Errorf("domain must be 1 to 64 characters long (got %d)", len(s))
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-' {
			continue
		}
		return errors.New("domain may hold only ASCII letters, digits, \"_\" and \"-\"")
	}
	return nil
}

// checkDomainUse refuses a domain where it would be ignored: on a rule that has
// no transform, on a transform that uses no key (fixed, nullify, keep), and on a
// rule with paths (the paths' rules carry their own keys, and take no domain).
func checkDomainUse(r Rule, reg *Registry) error {
	if r.Domain == "" {
		return nil
	}
	switch {
	case len(r.Paths) > 0:
		return errors.New("domain cannot be given to a rule with paths")
	case r.Use == "":
		return errors.New("domain needs a transform (use)")
	}
	if t, ok := reg.Lookup(r.Use); ok && !t.NeedsSalt {
		return fmt.Errorf("domain has no effect on transform %q, which uses no key", t.Name)
	}
	return nil
}

// sameRule says whether two columns are transformed in the same way: the same
// transform with the same arguments and the same domain (none counts as a
// domain). A missing rule, a rule with paths and one that only marks quasi are
// never the same as anything.
func sameRule(a, b Rule, bHas bool) bool {
	if !bHas || a.Use == "" || b.Use == "" || len(a.Paths) > 0 || len(b.Paths) > 0 {
		return false
	}
	if a.Use != b.Use || a.Domain != b.Domain {
		return false
	}
	return len(a.Args) == len(b.Args) && (len(a.Args) == 0 || reflect.DeepEqual(a.Args, b.Args))
}

// buildRule prepares the transform of a rule for its column. A transform that
// is registered without a Build (tests) is checked no further.
func buildRule(e Entry, t Transform, col Column, cols []Column, salt *Salt) (Func, error) {
	key, err := keyOf(t, salt, e.Rule.Domain)
	if err != nil {
		return nil, err
	}
	if t.Build == nil {
		return nil, nil
	}
	return t.Build(BuildContext{
		Args:    e.Rule.Args,
		Column:  col,
		Columns: cols,
		Key:     key,
	})
}

// tableColumns returns the columns of a table that are in a row (the generated
// ones are not), with their position in it.
func tableColumns(tbl *CatalogTable) []Column {
	var out []Column
	for _, c := range tbl.Columns {
		if c.Generated {
			continue
		}
		out = append(out, Column{
			Name: c.Name, TypeName: c.TypeName, ColumnType: c.ColumnType, DataType: c.DataType,
			CharacterSet: c.CharacterSet, Nullable: c.Nullable, Index: len(out),
		})
	}
	return out
}

func columnOf(cols []Column, name string) Column {
	for _, c := range cols {
		if strings.EqualFold(c.Name, name) {
			return c
		}
	}
	return Column{Name: name, Index: -1}
}
