package kanon

import (
	"bytes"
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

	"mysqldance/internal/mask"
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

// Run on a server: the report counts what the table holds (NULLs in one group,
// 'A' and 'a' in two), a table with no mark is left out, and nothing that is
// printed holds a value of the table.
func TestRunOnAServer(t *testing.T) {
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
		"CREATE TABLE people (id INT PRIMARY KEY, zip CHAR(8) NOT NULL, born DATE NULL, name VARCHAR(20))",
		"INSERT INTO people VALUES (1,'SECRETa','2000-01-01','n1'),(2,'SECRETa','2000-01-01','n2'),(3,'secreta','2000-01-01','n3'),(4,'SECRETb',NULL,'n4'),(5,'SECRETb',NULL,'n5'),(6,'SECRETb',NULL,'n6')",
		"CREATE TABLE other (id INT PRIMARY KEY, v INT)",
		"CREATE TABLE empty_t (z INT)",
	} {
		if _, err := conn.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	rules, err := mask.Parse([]byte("version: 1\nrules:\n  "+db+".people.zip:\n    quasi: true\n  "+db+".people.born:\n    quasi: true\n"+
		"  "+db+".empty_t.z:\n    quasi: true\n  "+db+".other.v:\n    use: nullify\n"), mask.DefaultRegistry())
	if err != nil {
		t.Fatal(err)
	}
	var log bytes.Buffer
	opt := Options{Source: cfg, DBs: []string{db}, Rules: rules, K: 3}
	got, err := Run(ctx, opt)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// 'SECRETa' x2 and 'secreta' x1 are two groups (a case-insensitive grouping
	// would make one of 3); 'SECRETb' with NULL is one group of 3.
	want := []string{"empty_t:0:0:-", "people:6:3:1"}
	var have []string
	for _, r := range got {
		min := "-"
		if r.MinK != nil {
			min = strconv.FormatInt(*r.MinK, 10)
		}
		have = append(have, strings.Join([]string{r.Table, strconv.FormatInt(r.Rows, 10), strconv.FormatInt(r.Groups, 10), min}, ":"))
	}
	if strings.Join(have, " ") != strings.Join(want, " ") {
		t.Fatalf("reports = %v, want %v", have, want)
	}
	people := got[1]
	if people.Dist.K1 != 1 || people.Dist.K2 != 2 || people.Dist.K3 != 3 {
		t.Errorf("distribution = %+v", people.Dist)
	}
	if people.Below == nil || *people.Below != 3 || !AnyBelow(got) {
		t.Errorf("rows under k = 3: %v, want 3 (the group of 2 and the group of 1)", people.Below)
	}
	if got[0].Below == nil || *got[0].Below != 0 {
		t.Errorf("an empty table has no rows under k: %v", got[0].Below)
	}
	if err := WriteTable(&log, got, true); err != nil {
		t.Fatal(err)
	}
	if err := WriteJSON(&log, got); err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{"SECRET", "secret", "2000-01-01", "n1"} {
		if strings.Contains(log.String(), v) {
			t.Errorf("the output holds the value %q:\n%s", v, log.String())
		}
	}
	// A run that fails (a rule for a missing column) measures nothing.
	bad, err := mask.Parse([]byte("version: 1\nrules:\n  "+db+".people.nope:\n    quasi: true\n"), mask.DefaultRegistry())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Run(ctx, Options{Source: cfg, DBs: []string{db}, Rules: bad}); err == nil || !strings.Contains(err.Error(), "no such column") {
		t.Errorf("Run with a bad rule: %v", err)
	}
}
