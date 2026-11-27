package scanrun

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"log/slog"
	"net"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/go-sql-driver/mysql"

	"mysqldance/internal/source"
)

// testServer returns the config and DSN of MYSQLDANCE_TEST_DSN, and skips the
// test without it.
func testServer(t *testing.T) (source.Config, string) {
	t.Helper()
	dsn := os.Getenv("MYSQLDANCE_TEST_DSN")
	if dsn == "" {
		t.Skip("MYSQLDANCE_TEST_DSN is not set")
	}
	mc, err := mysql.ParseDSN(dsn)
	if err != nil {
		t.Fatalf("parse MYSQLDANCE_TEST_DSN: %v", err)
	}
	cfg := source.Config{User: mc.User, Password: mc.Passwd, SSLMode: os.Getenv("MYSQLDANCE_TEST_SSL_MODE")}
	if mc.Net == "unix" {
		cfg.Socket = mc.Addr
		return cfg, dsn
	}
	host, port, err := net.SplitHostPort(mc.Addr)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Host = host
	if cfg.Port, err = strconv.Atoi(port); err != nil {
		t.Fatal(err)
	}
	return cfg, dsn
}

// Run on a server: known addresses and phone numbers are found, in tables with
// and without a key; binary, bit and generated columns, views and excluded
// tables are not read; and neither the report nor the log holds a value.
func TestExecuteOnAServer(t *testing.T) {
	cfg, dsn := testServer(t)
	admin, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	buf := make([]byte, 6)
	if _, err := rand.Read(buf); err != nil {
		t.Fatal(err)
	}
	db := "mysqldance_test_" + hex.EncodeToString(buf)
	ctx := context.Background()
	conn, err := admin.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = conn.ExecContext(context.Background(), "DROP DATABASE IF EXISTS `"+db+"`")
		_ = conn.Close()
	})
	for _, stmt := range []string{
		"CREATE DATABASE `" + db + "`",
		"USE `" + db + "`",
		// users has a key; contact has none (EachRow would sort it through extsort).
		"CREATE TABLE users (id INT PRIMARY KEY, mail VARCHAR(60), tel VARCHAR(20), memo TEXT, raw BLOB, flags BIT(8), gen VARCHAR(60) AS (CONCAT(mail, 'x')) STORED)",
		"INSERT INTO users (id, mail, tel, memo, raw, flags) VALUES " +
			"(1,'alice.secret@example.com','090-1234-5678','write to bob.secret@example.org or carol.secret@example.org','dave.secret@example.net',b'1')," +
			"(2,NULL,'not a number',NULL,'erin.secret@example.net',b'0')," +
			"(3,'plain','03-1234-5678','nothing',NULL,NULL)",
		"CREATE TABLE contact (mail VARCHAR(60), kind ENUM('a','b'))",
		"INSERT INTO contact VALUES ('frank.secret@example.com','a'),('frank.secret@example.com','b'),(NULL,'a')",
		"CREATE TABLE skipped (mail VARCHAR(60))",
		"INSERT INTO skipped VALUES ('gina.secret@example.com')",
		"CREATE VIEW v_users AS SELECT mail FROM users",
	} {
		if _, err := conn.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	got, err := Execute(ctx, Options{
		Source: cfg, DBs: []string{db}, ExcludeTables: []string{db + ".skipped"},
		Config: piiConfig(t), Log: log,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var have []string
	for _, f := range got {
		have = append(have, strings.Join([]string{f.Table, f.Column, f.Rule, strconv.FormatInt(f.Rows, 10), strconv.FormatInt(f.Scanned, 10)}, ":"))
	}
	want := []string{
		"contact:mail:pii-email:2:2",
		"users:mail:pii-email:1:2",
		"users:memo:pii-email:1:2",
		"users:tel:pii-phone-jp:2:3",
	}
	if strings.Join(have, " ") != strings.Join(want, " ") {
		t.Fatalf("findings = %v\nwant       %v", have, want)
	}
	var out bytes.Buffer
	if err := WriteTable(&out, got); err != nil {
		t.Fatal(err)
	}
	if err := WriteJSON(&out, got); err != nil {
		t.Fatal(err)
	}
	// A failed run names the table and holds no value either.
	_, err = Execute(ctx, Options{Source: cfg, DBs: []string{db + "_missing"}, Config: piiConfig(t), Log: log})
	if err == nil {
		t.Error("a missing database was scanned")
	} else {
		out.WriteString(err.Error())
	}
	for _, v := range []string{"secret", "example.", "090-", "-1234-", "alice", "bob", "carol", "dave", "erin", "frank", "gina"} {
		if strings.Contains(out.String(), v) || strings.Contains(logs.String(), v) {
			t.Errorf("the output or the log holds %q:\nout:\n%s\nlog:\n%s", v, out.String(), logs.String())
		}
	}
}
