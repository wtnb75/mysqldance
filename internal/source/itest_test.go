package source

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"net"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/go-sql-driver/mysql"

	"mysqldance/internal/sqlvalue"
)

// Integration test helpers. Tests that need a server are skipped unless
// MYSQLDANCE_TEST_DSN is set, e.g. root:pw@tcp(127.0.0.1:3306)/

// testConfig converts MYSQLDANCE_TEST_DSN into a Config and also returns the
// DSN for an administrative *sql.DB. It skips the test when the variable is empty.
func testConfig(t *testing.T) (Config, string) {
	t.Helper()
	dsn := os.Getenv("MYSQLDANCE_TEST_DSN")
	if dsn == "" {
		t.Skip("MYSQLDANCE_TEST_DSN is not set")
	}
	mc, err := mysql.ParseDSN(dsn)
	if err != nil {
		t.Fatalf("parse MYSQLDANCE_TEST_DSN: %v", err)
	}
	// MYSQLDANCE_TEST_SSL_MODE=disabled for a server that offers no TLS 1.2
	// (MySQL 5.7.21); the tests of TLS itself then fail, as they should.
	cfg := Config{User: mc.User, Password: mc.Passwd, SSLMode: os.Getenv("MYSQLDANCE_TEST_SSL_MODE")}
	if mc.Net == "unix" {
		cfg.Socket = mc.Addr
		return cfg, dsn
	}
	host, port, err := net.SplitHostPort(mc.Addr)
	if err != nil {
		t.Fatalf("parse address %q: %v", mc.Addr, err)
	}
	cfg.Host = host
	if cfg.Port, err = strconv.Atoi(port); err != nil {
		t.Fatalf("parse port %q: %v", port, err)
	}
	return cfg, dsn
}

// testDB creates a uniquely named database dropped at cleanup. It returns the
// connection config, the database name and a single pinned connection to it
// for setting up fixtures (so session settings such as time_zone stick).
func testDB(t *testing.T) (Config, string, *sql.Conn) {
	t.Helper()
	cfg, dsn := testConfig(t)
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	buf := make([]byte, 6)
	if _, err := rand.Read(buf); err != nil {
		t.Fatal(err)
	}
	name := "mysqldance_test_" + hex.EncodeToString(buf)
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := conn.ExecContext(context.Background(), "DROP DATABASE IF EXISTS `"+name+"`"); err != nil {
			t.Errorf("drop database %s: %v", name, err)
		}
		_ = conn.Close()
	})
	mustExec(t, conn, "CREATE DATABASE `"+name+"`", "USE `"+name+"`")
	return cfg, name, conn
}

// isMariaDB reports whether the test server is MariaDB. Tests use it only to
// skip checks written in MySQL's SHOW CREATE or JSON text, or to run a check
// of MariaDB-only behavior; production code never asks.
func isMariaDB(t *testing.T, conn *sql.Conn) bool {
	t.Helper()
	var v string
	if err := conn.QueryRowContext(context.Background(), "SELECT VERSION()").Scan(&v); err != nil {
		t.Fatal(err)
	}
	return strings.Contains(strings.ToLower(v), "mariadb")
}

// isMySQL57 reports whether the test server is MySQL 5.7. Tests use it only to
// skip checks of what 5.7 lacks (INVISIBLE columns, functional keys, CHECK,
// show_create_table_verbosity, utf8mb4_0900 collations).
func isMySQL57(t *testing.T, conn *sql.Conn) bool {
	t.Helper()
	return strings.HasPrefix(serverVersion(t, conn), "5.7.")
}

// isMySQL56 reports whether the test server is MySQL 5.6. Tests use it only to
// skip checks of what 5.6 lacks (JSON, generated columns, several triggers for
// one event, user names over 16 characters).
func isMySQL56(t *testing.T, conn *sql.Conn) bool {
	t.Helper()
	return strings.HasPrefix(serverVersion(t, conn), "5.6.")
}

// isMySQL5 reports whether the test server is MySQL 5.6 or 5.7, which have no
// INVISIBLE columns, functional keys, CHECK, show_create_table_verbosity
// (5.7.21 and 5.6) or utf8mb4_0900 collations.
func isMySQL5(t *testing.T, conn *sql.Conn) bool {
	t.Helper()
	return isMySQL56(t, conn) || isMySQL57(t, conn)
}

// testUser names an account of a test: prefix and the random part of the name
// of db, cut to 16 characters, the limit of MySQL 5.6 (5.7 allows 32).
func testUser(prefix, db string) string {
	id := strings.TrimPrefix(db, "mysqldance_test_")
	return prefix + id[:min(len(id), max(0, 16-len(prefix)))]
}

// serverVersion returns SELECT VERSION() of the test server. It is not shared
// with the other test packages on purpose (R-072 in docs/review-decisions.md).
func serverVersion(t *testing.T, conn *sql.Conn) string {
	t.Helper()
	var v string
	if err := conn.QueryRowContext(context.Background(), "SELECT VERSION()").Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

// skipOnMySQL56 skips the test on MySQL 5.6, whose fixture needs what 5.6 lacks.
func skipOnMySQL56(t *testing.T, conn *sql.Conn, what string) {
	t.Helper()
	if isMySQL56(t, conn) {
		t.Skipf("MySQL 5.6 has no %s", what)
	}
}

func mustExec(t *testing.T, conn *sql.Conn, stmts ...string) {
	t.Helper()
	for _, s := range stmts {
		if _, err := conn.ExecContext(context.Background(), s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
}

// openSnapshot opens a snapshot of db and closes it at cleanup.
func openSnapshot(t *testing.T, cfg Config, db string) *Snapshot {
	t.Helper()
	s, err := Open(context.Background(), cfg, db)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return s
}

// warningsOf returns the warnings from Tables().
func warningsOf(t *testing.T, s *Snapshot) []string {
	t.Helper()
	_, warnings, err := s.Tables(context.Background())
	if err != nil {
		t.Fatalf("Tables: %v", err)
	}
	return warnings
}

// tableByName returns the TableInfo named name from Tables().
func tableByName(t *testing.T, s *Snapshot, name string) TableInfo {
	t.Helper()
	tables, _, err := s.Tables(context.Background())
	if err != nil {
		t.Fatalf("Tables: %v", err)
	}
	for _, ti := range tables {
		if ti.Name == name {
			return ti
		}
	}
	t.Fatalf("table %q not found in %v", name, tables)
	return TableInfo{}
}

// rowsOf reads the formatted rows of table name.
func rowsOf(t *testing.T, s *Snapshot, name string) [][]string {
	t.Helper()
	rows, err := s.Rows(context.Background(), tableByName(t, s, name))
	if err != nil {
		t.Fatalf("Rows(%s): %v", name, err)
	}
	// Render the typed values as MySQL literals so the assertions compare the
	// bytes that end up in the output.
	out := make([][]string, len(rows))
	for i, row := range rows {
		out[i] = sqlvalue.Literals(row)
	}
	return out
}
