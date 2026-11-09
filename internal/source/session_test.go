package source

import (
	"context"
	"database/sql"
	"reflect"
	"strings"
	"testing"

	"github.com/go-sql-driver/mysql"
)

// TestSessionSettingsPinned opens a snapshot on a connection whose session
// starts with a hostile sql_mode (set by the driver on connect, never
// globally), so the test fails whatever the server default is unless start()
// resets the session settings that change SHOW CREATE output and values.
func TestSessionSettingsPinned(t *testing.T) {
	_, db, conn := testDB(t)
	mustExec(t, conn,
		"CREATE TABLE t (id INT PRIMARY KEY, c CHAR(5))",
		"INSERT INTO t VALUES (1, 'ab')",
		"CREATE VIEW v AS SELECT id, c FROM t",
	)

	_, dsn := testConfig(t)
	mc, err := mysql.ParseDSN(dsn)
	if err != nil {
		t.Fatal(err)
	}
	mc.InterpolateParams = true
	mc.Params = map[string]string{
		"sql_mode":              "'ANSI_QUOTES,PAD_CHAR_TO_FULL_LENGTH'",
		"sql_quote_show_create": "0",
	}
	connector, err := mysql.NewConnector(mc)
	if err != nil {
		t.Fatal(err)
	}
	sdb := sql.OpenDB(connector)
	ctx := context.Background()
	s, err := start(ctx, sdb, db)
	if err != nil {
		_ = sdb.Close()
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})

	var mode string
	var quote int
	if err := s.conn.QueryRowContext(ctx,
		"SELECT @@session.sql_mode, @@session.sql_quote_show_create").Scan(&mode, &quote); err != nil {
		t.Fatal(err)
	}
	if mode != "" || quote != 1 {
		t.Errorf("snapshot session sql_mode = %q, sql_quote_show_create = %d; want '' and 1", mode, quote)
	}

	ti := tableByName(t, s, "t")
	if !strings.HasPrefix(ti.Create, "CREATE TABLE `t` (\n  `id` int") || strings.Contains(ti.Create, `"`) {
		t.Errorf("table Create = %q, want backtick-quoted identifiers", ti.Create)
	}
	views, _, err := s.Views(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 1 || !strings.Contains(views[0].Create, "VIEW `v` AS select `t`.`id`") ||
		strings.Contains(views[0].Create, `"`) {
		t.Errorf("Views = %q, want backtick-quoted identifiers", views)
	}
	create, err := s.CreateDatabase(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(create, "CREATE DATABASE `"+db+"`") {
		t.Errorf("CreateDatabase = %q, want backtick-quoted name", create)
	}
	// PAD_CHAR_TO_FULL_LENGTH would return 'ab   '.
	if got, want := rowsOf(t, s, "t"), [][]string{{"1", "'ab'"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("rows = %v, want %v", got, want)
	}
}
