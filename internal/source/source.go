// Package source reads a consistent snapshot of one MySQL database: its
// CREATE statements, table metadata and rows formatted as SQL literals.
package source

import (
	"database/sql"
	"log/slog"
	"time"

	"mysqldance/internal/extsort"
	"mysqldance/internal/sqlvalue"
)

// TableInfo describes a base table.
type TableInfo struct {
	Name, Create, Engine string
	// Columns lists the non-generated columns in ordinal order.
	Columns []sqlvalue.Column
	// ListColumns is true when the table has generated or INVISIBLE columns,
	// so INSERT statements must name their columns.
	ListColumns bool
	// OrderBy is the primary key, else the first NOT NULL unique key by
	// index name, else empty.
	OrderBy []string
	// OrderByTypes holds the information_schema DATA_TYPE of each OrderBy
	// column, in the same order (same length as OrderBy). Rows uses it to
	// pick the sort expression.
	OrderByTypes []string
	// ApproxRows is the TABLE_ROWS estimate.
	ApproxRows int64
	// Def is the structure of the table. Tables does not fill it; the caller
	// sets it from Defs for a writer that builds its definitions from structure,
	// so that the writer sees it on every call without remembering anything.
	Def *TableDef
}

// Snapshot is a read-only consistent snapshot, held on a pinned connection
// inside one transaction. It reads one database at a time (the current one);
// Use switches databases without leaving the snapshot.
//
// With Config.Jobs above 1 the same point in time is also held by more
// connections, each a Snapshot of its own (see Readers). A Snapshot is for one
// goroutine at a time; the readers may run in parallel with each other.
type Snapshot struct {
	db      *sql.DB
	conn    *sql.Conn
	name    string
	dialect dialect
	// locked is true while the global read lock is held; noLock is the NoLock
	// option. A non-InnoDB table read with neither is an error.
	locked, noLock bool
	// log gets the messages of Open and Close. lockedAt is when the global read
	// lock was taken; it is set while that lock is held until Close, on the first
	// reader only, so Close can report how long it was held.
	log      *slog.Logger
	lockedAt time.Time
	// others are the other readers, and lockConn is the connection that holds
	// the global read lock while the snapshots start. Both are set on the first
	// reader only (Config.Jobs above 1).
	others   []*Snapshot
	lockConn *sql.Conn
	// tableList is the result of listTables for name, kept so Tables, Views and
	// Defs read the catalog once; use drops it. tableListed tells a cached empty
	// list from none.
	tableList   []tableRow
	tableListed bool
	// sortMemory and sortFile are Config.SortMemory and Config.SortFile.
	sortMemory int64
	sortFile   extsort.NewFile
	// largeTableRows is Config.LargeTableRows with its default applied.
	largeTableRows int64
}

// Readers returns the connections that can read rows in parallel: the
// Snapshot itself first, then Config.Jobs-1 others. Each one has the same
// current database as the Snapshot (Use switches them all) and sees the same
// point in time. The metadata methods (CreateDatabase, Tables, Views, Defs)
// are for the Snapshot itself; Rows may run on any reader.
func (s *Snapshot) Readers() []*Snapshot {
	return append([]*Snapshot{s}, s.others...)
}
