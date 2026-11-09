package source

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"net"
	"os"
	"strconv"
	"testing"

	"github.com/go-sql-driver/mysql"
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
	cfg := Config{User: mc.User, Password: mc.Passwd}
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

// tableByName returns the TableInfo named name from Tables().
func tableByName(t *testing.T, s *Snapshot, name string) TableInfo {
	t.Helper()
	tables, err := s.Tables(context.Background())
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
	return rows
}
