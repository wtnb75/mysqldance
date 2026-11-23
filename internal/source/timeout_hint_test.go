package source

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"

	"mysqldance/internal/mask"
	"mysqldance/internal/sqlvalue"
)

// brokenConnector hands out connections whose every statement fails the way
// the driver reports a read timeout: only as mysql.ErrInvalidConn.
type brokenConnector struct{}

func (brokenConnector) Connect(context.Context) (driver.Conn, error) { return brokenConn{}, nil }
func (brokenConnector) Driver() driver.Driver                        { return nil }

type brokenConn struct{}

func (brokenConn) Prepare(string) (driver.Stmt, error) { return nil, mysql.ErrInvalidConn }
func (brokenConn) Close() error                        { return nil }
func (brokenConn) Begin() (driver.Tx, error)           { return nil, mysql.ErrInvalidConn }
func (brokenConn) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	return nil, mysql.ErrInvalidConn
}
func (brokenConn) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	return nil, mysql.ErrInvalidConn
}

// brokenSnapshot is a Snapshot whose server stops answering after Open.
func brokenSnapshot(t *testing.T, readTimeout time.Duration) *Snapshot {
	t.Helper()
	db := sql.OpenDB(brokenConnector{})
	t.Cleanup(func() { _ = db.Close() })
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return &Snapshot{db: db, conn: conn, name: "db", noLock: true, readTimeout: readTimeout}
}

// Every read after Open must name the read timeout when the connection breaks,
// as Open does, so the user can trace "invalid connection" back to it.
func TestReadsAfterOpenNameReadTimeout(t *testing.T) {
	ctx := context.Background()
	tbl := TableInfo{Name: "t", Columns: []sqlvalue.Column{{Name: "id"}}}
	calls := map[string]func(s *Snapshot) error{
		"CreateDatabase": func(s *Snapshot) error { _, err := s.CreateDatabase(ctx); return err },
		"Tables":         func(s *Snapshot) error { _, _, err := s.Tables(ctx); return err },
		"Views":          func(s *Snapshot) error { _, _, err := s.Views(ctx); return err },
		"Defs":           func(s *Snapshot) error { _, err := s.Defs(ctx); return err },
		"Triggers":       func(s *Snapshot) error { _, err := s.Triggers(ctx); return err },
		"Routines":       func(s *Snapshot) error { _, err := s.Routines(ctx); return err },
		"Events":         func(s *Snapshot) error { _, err := s.Events(ctx); return err },
		"Objects":        func(s *Snapshot) error { _, err := s.Objects(ctx); return err },
		"Use":            func(s *Snapshot) error { return s.Use(ctx, "db") },
		"FoldsTableCase": func(s *Snapshot) error { _, err := s.FoldsTableCase(ctx); return err },
		// MaskCatalog calls FoldsTableCase: the hint is applied twice, shown once.
		"MaskCatalog": func(s *Snapshot) error { _, err := s.MaskCatalog(ctx, []string{"db"}); return err },
		"GroupSizes": func(s *Snapshot) error {
			_, err := s.GroupSizes(ctx, "t", []mask.QuasiColumn{{Name: "id", DataType: "int"}})
			return err
		},
		"Rows": func(s *Snapshot) error { _, err := s.Rows(ctx, tbl); return err },
		"EachRow": func(s *Snapshot) error {
			return s.EachRow(ctx, tbl, func([]sqlvalue.Value) error { return nil })
		},
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			err := call(brokenSnapshot(t, 45*time.Second))
			if err == nil {
				t.Fatal("call succeeded on a broken connection")
			}
			if !errors.Is(err, mysql.ErrInvalidConn) {
				t.Errorf("err = %v, want it to still wrap the driver error", err)
			}
			if !strings.Contains(err.Error(), "read timeout of 45s") {
				t.Errorf("err = %v, want it to name the read timeout", err)
			}
			if n := strings.Count(err.Error(), "read timeout"); n != 1 {
				t.Errorf("err = %v, names the read timeout %d times, want once", err, n)
			}
		})
	}
}

// Without --read-timeout the error text must not change.
func TestReadsAfterOpenWithoutReadTimeoutAreUnchanged(t *testing.T) {
	_, _, err := brokenSnapshot(t, 0).Tables(context.Background())
	if err == nil {
		t.Fatal("Tables succeeded on a broken connection")
	}
	if strings.Contains(err.Error(), "read timeout") {
		t.Errorf("err = %v, want no mention of a read timeout", err)
	}
}

// Only a connection error gets the hint; a failure the driver can tell apart
// keeps its text.
func TestReadTimeoutHintOnlyForInvalidConn(t *testing.T) {
	plain := errors.New("Table 'db.t' doesn't exist")
	if got := withReadTimeoutHint(plain, time.Second); got != plain {
		t.Errorf("withReadTimeoutHint(plain) = %v, want it unchanged", got)
	}
	if got := withReadTimeoutHint(nil, time.Second); got != nil {
		t.Errorf("withReadTimeoutHint(nil) = %v, want nil", got)
	}
	// Nested methods each apply the hint; the text must carry it once, with the
	// timeout of the first one.
	once := withReadTimeoutHint(mysql.ErrInvalidConn, time.Second)
	twice := withReadTimeoutHint(once, time.Minute)
	if twice != once || strings.Count(twice.Error(), "read timeout") != 1 {
		t.Errorf("applied twice: %v, want the first hint only", twice)
	}
}
