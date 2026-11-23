package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"mysqldance/internal/datafile"
	"mysqldance/internal/dump"
	"mysqldance/internal/source"
	"mysqldance/internal/writer"
)

func noEnv(string) string { return "" }

func TestRunUsageErrors(t *testing.T) {
	for _, tt := range []struct {
		name string
		args []string
	}{
		{"no output", []string{"db1"}},
		{"no database", []string{"-o", t.TempDir()}},
		{"password flag", []string{"--password", "x", "-o", t.TempDir(), "db1"}},
		{"unknown flag", []string{"--bogus", "-o", t.TempDir(), "db1"}},
		{"zero rows per insert", []string{"--rows-per-insert", "0", "-o", t.TempDir(), "db1"}},
		{"negative rows per file", []string{"--rows-per-file", "-1", "-o", t.TempDir(), "db1"}},
		{"too large rows per file", []string{"--rows-per-file", "4294967296", "-o", t.TempDir(), "db1"}},
		{"non-octal file mode", []string{"--file-mode", "rw", "-o", t.TempDir(), "db1"}},
		{"file mode with special bits", []string{"--file-mode", "4755", "-o", t.TempDir(), "db1"}},
		{"file mode not readable by owner", []string{"--file-mode", "070", "-o", t.TempDir(), "db1"}},
		{"dir mode without owner rwx", []string{"--dir-mode", "600", "-o", t.TempDir(), "db1"}},
		{"zero max insert bytes", []string{"--max-insert-bytes", "0", "-o", t.TempDir(), "db1"}},
		{"unknown ssl mode", []string{"--ssl-mode", "bogus", "-o", t.TempDir(), "db1"}},
		{"verify-ca without ssl-ca", []string{"--ssl-mode", "verify-ca", "-o", t.TempDir(), "db1"}},
		{"ssl-ca with default mode", []string{"--ssl-ca", "ca.pem", "-o", t.TempDir(), "db1"}},
		{"ssl-ca with disabled", []string{"--ssl-mode", "disabled", "--ssl-ca", "ca.pem", "-o", t.TempDir(), "db1"}},
		{"missing password file", []string{"--password-file", filepath.Join(t.TempDir(), "none"), "-o", t.TempDir(), "db1"}},
		{"unknown target", []string{"--target", "oracle", "-o", t.TempDir(), "db1"}},
		{"empty target", []string{"--target", "", "-o", t.TempDir(), "db1"}},
		{"zero jobs", []string{"--jobs", "0", "-o", t.TempDir(), "db1"}},
		{"negative jobs", []string{"-j", "-2", "-o", t.TempDir(), "db1"}},
		{"too many jobs", []string{"--jobs", "65", "-o", t.TempDir(), "db1"}},
		{"jobs with no-lock", []string{"--jobs", "2", "--no-lock", "-o", t.TempDir(), "db1"}},
		{"negative lock timeout", []string{"--lock-timeout", "-1s", "-o", t.TempDir(), "db1"}},
		{"lock timeout without unit", []string{"--lock-timeout", "30", "-o", t.TempDir(), "db1"}},
		{"negative connect timeout", []string{"--connect-timeout", "-1s", "-o", t.TempDir(), "db1"}},
		{"connect timeout without unit", []string{"--connect-timeout", "30", "-o", t.TempDir(), "db1"}},
		{"negative read timeout", []string{"--read-timeout", "-1s", "-o", t.TempDir(), "db1"}},
		{"read timeout without unit", []string{"--read-timeout", "30", "-o", t.TempDir(), "db1"}},
		{"negative progress interval", []string{"--progress-interval", "-1s", "-o", t.TempDir(), "db1"}},
		{"progress interval without unit", []string{"--progress-interval", "5", "-o", t.TempDir(), "db1"}},
		{"unreadable progress interval", []string{"--progress-interval", "soon", "-o", t.TempDir(), "db1"}},
		{"unknown progress mode", []string{"--progress", "fancy", "-o", t.TempDir(), "db1"}},
		{"empty progress mode", []string{"--progress", "", "-o", t.TempDir(), "db1"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var stderr bytes.Buffer
			if code := run(tt.args, noEnv, io.Discard, &stderr); code != 1 {
				t.Errorf("run(%q) = %d, want 1", tt.args, code)
			}
			if stderr.Len() == 0 {
				t.Errorf("run(%q) wrote nothing to stderr", tt.args)
			}
		})
	}
}

func TestParseArgsDefaults(t *testing.T) {
	got, err := parseArgs([]string{"-o", "out", "db1", "db2"}, noEnv)
	if err != nil {
		t.Fatalf("parseArgs: %v", err)
	}
	want := dump.Options{
		Source: source.Config{Host: "127.0.0.1", Port: 3306, SSLMode: "preferred", Jobs: 1, LockTimeout: source.DefaultLockTimeout, ConnectTimeout: source.DefaultConnectTimeout},
		DBs:    []string{"db1", "db2"},
		OutDir: "out",
		Data:   datafile.Options{RowsPerInsert: 1000, MaxInsertBytes: 1048576},

		ProgressInterval: dump.DefaultProgressInterval,
	}
	// The logger is checked in logflags_test.go.
	if got.Log == nil {
		t.Error("Options.Log is nil")
	}
	got.Log = nil
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseArgs = %+v, want %+v", got, want)
	}
}

func TestParseArgsExcludeTable(t *testing.T) {
	got, err := parseArgs([]string{"--exclude-table", "db1.sessions", "--exclude-table", "db2.audit.log", "-o", "out", "db1", "db2"}, noEnv)
	if err != nil {
		t.Fatalf("parseArgs: %v", err)
	}
	// Repeated flags are kept in the order given; the first dot splits the pair.
	if want := []string{"db1.sessions", "db2.audit.log"}; !reflect.DeepEqual(got.ExcludeTables, want) {
		t.Errorf("ExcludeTables = %q, want %q", got.ExcludeTables, want)
	}
	// Without the flag the list is nil, so the options are as before.
	got, err = parseArgs([]string{"-o", "out", "db1"}, noEnv)
	if err != nil {
		t.Fatalf("parseArgs: %v", err)
	}
	if got.ExcludeTables != nil {
		t.Errorf("ExcludeTables = %q, want nil", got.ExcludeTables)
	}
}

func TestParseArgsExcludeTableUsageErrors(t *testing.T) {
	for _, spec := range []string{"", "sessions", ".sessions", "db.", "."} {
		var stderr bytes.Buffer
		_, err := parseArgsTo([]string{"--exclude-table", spec, "-o", "out", "db1"}, noEnv, &stderr)
		if !errors.Is(err, errUsage) {
			t.Errorf("--exclude-table %q: err = %v, want errUsage", spec, err)
		}
		if !strings.Contains(stderr.String(), "--exclude-table") || !strings.Contains(stderr.String(), spec) {
			t.Errorf("--exclude-table %q: stderr does not name the flag and the value:\n%s", spec, stderr.String())
		}
	}
}

func TestParseArgsCaseSafeNames(t *testing.T) {
	for _, target := range []string{"mysql", "sqlite", "postgres"} {
		got, err := parseArgs([]string{"--target", target, "--case-safe-names", "-o", "out", "db1"}, noEnv)
		if err != nil {
			t.Fatalf("--target %s: parseArgs: %v", target, err)
		}
		if !got.CaseSafeNames {
			t.Errorf("--target %s: CaseSafeNames is off", target)
		}
	}
	got, err := parseArgs([]string{"-o", "out", "db1"}, noEnv)
	if err != nil {
		t.Fatalf("parseArgs: %v", err)
	}
	if got.CaseSafeNames {
		t.Error("CaseSafeNames is on by default")
	}
}

func TestParseArgsDefiner(t *testing.T) {
	for _, tt := range []struct {
		args []string
		want bool
	}{
		{nil, false},
		{[]string{"--definer=strip"}, false},
		{[]string{"--definer=keep"}, true},
		{[]string{"--definer", "keep"}, true},
		// No views, triggers, routines or events are written for these targets.
		{[]string{"--target", "sqlite", "--definer=keep"}, false},
		{[]string{"--target", "postgres", "--definer=keep"}, false},
	} {
		got, err := parseArgs(append(tt.args, "-o", "out", "db1"), noEnv)
		if err != nil {
			t.Fatalf("%v: parseArgs: %v", tt.args, err)
		}
		if got.KeepDefiner != tt.want {
			t.Errorf("%v: KeepDefiner = %v, want %v", tt.args, got.KeepDefiner, tt.want)
		}
	}
}

func TestParseArgsDefinerUsageError(t *testing.T) {
	for _, v := range []string{"", "drop", "KEEP"} {
		var stderr bytes.Buffer
		_, err := parseArgsTo([]string{"--definer=" + v, "-o", "out", "db1"}, noEnv, &stderr)
		if !errors.Is(err, errUsage) {
			t.Errorf("--definer=%q: err = %v, want errUsage", v, err)
		}
		if !strings.Contains(stderr.String(), "--definer") {
			t.Errorf("--definer=%q: stderr does not name the flag:\n%s", v, stderr.String())
		}
	}
}

func TestParseArgsPGSchema(t *testing.T) {
	got, err := parseArgs([]string{"--target", "postgres", "--pg-schema", "pg_a=app", "--pg-schema", "pg_b=bee", "-o", "out", "pg_a", "pg_b", "c"}, noEnv)
	if err != nil {
		t.Fatalf("parseArgs: %v", err)
	}
	var b strings.Builder
	if err := got.Writer.WriteSchema(&b, writer.Schema{DB: "pg_a"}); err != nil {
		t.Fatalf("WriteSchema: %v", err)
	}
	if !strings.Contains(b.String(), `CREATE SCHEMA IF NOT EXISTS "app";`) {
		t.Errorf("the schema name was not applied:\n%s", b.String())
	}
}

func TestParseArgsPGSchemaUsageErrors(t *testing.T) {
	long := strings.Repeat("x", 64)
	for name, args := range map[string][]string{
		"not postgres":    {"--pg-schema", "pg_a=a", "-o", "out", "pg_a"},
		"sqlite":          {"--target", "sqlite", "--pg-schema", "pg_a=a", "-o", "out", "pg_a"},
		"no equal sign":   {"--target", "postgres", "--pg-schema", "pg_a", "-o", "out", "pg_a"},
		"db not dumped":   {"--target", "postgres", "--pg-schema", "x=a", "-o", "out", "pg_a"},
		"given twice":     {"--target", "postgres", "--pg-schema", "pg_a=a", "--pg-schema", "pg_a=b", "-o", "out", "pg_a"},
		"same schema":     {"--target", "postgres", "--pg-schema", "pg_a=s", "--pg-schema", "pg_b=s", "-o", "out", "pg_a", "pg_b"},
		"pg_ schema":      {"--target", "postgres", "--pg-schema", "pg_a=pg_b", "-o", "out", "pg_a"},
		"over 63 bytes":   {"--target", "postgres", "--pg-schema", "pg_a=" + long, "-o", "out", "pg_a"},
		"clash with a db": {"--target", "postgres", "--pg-schema", "pg_a=c", "-o", "out", "pg_a", "c"},
		"empty schema":    {"--target", "postgres", "--pg-schema", "pg_a=", "-o", "out", "pg_a"},
		"empty value":     {"--target", "postgres", "--pg-schema", "", "-o", "out", "pg_a"},
	} {
		t.Run(name, func(t *testing.T) {
			var stderr bytes.Buffer
			_, err := parseArgsTo(args, noEnv, &stderr)
			if !errors.Is(err, errUsage) {
				t.Errorf("err = %v, want errUsage", err)
			}
			if !strings.Contains(stderr.String(), "--pg-schema") {
				t.Errorf("stderr does not name the flag:\n%s", stderr.String())
			}
		})
	}
}

func TestParseArgsLockTimeout(t *testing.T) {
	for _, tt := range []struct {
		args []string
		want time.Duration
	}{
		{nil, 30 * time.Second},
		{[]string{"--lock-timeout", "5s"}, 5 * time.Second},
		{[]string{"--lock-timeout", "1m30s"}, 90 * time.Second},
		{[]string{"--lock-timeout", "0"}, 0},
	} {
		got, err := parseArgs(append(tt.args, "-o", "out", "d"), noEnv)
		if err != nil {
			t.Fatalf("parseArgs(%q): %v", tt.args, err)
		}
		if got.Source.LockTimeout != tt.want {
			t.Errorf("parseArgs(%q): LockTimeout = %v, want %v", tt.args, got.Source.LockTimeout, tt.want)
		}
	}
}

func TestParseArgsConnectAndReadTimeout(t *testing.T) {
	for _, tt := range []struct {
		args        []string
		wantConnect time.Duration
		wantRead    time.Duration
	}{
		{nil, 30 * time.Second, 0},
		{[]string{"--connect-timeout", "5s"}, 5 * time.Second, 0},
		{[]string{"--connect-timeout", "0"}, 0, 0},
		{[]string{"--read-timeout", "2m"}, 30 * time.Second, 2 * time.Minute},
		{[]string{"--read-timeout", "0"}, 30 * time.Second, 0},
	} {
		got, err := parseArgs(append(tt.args, "-o", "out", "d"), noEnv)
		if err != nil {
			t.Fatalf("parseArgs(%q): %v", tt.args, err)
		}
		if got.Source.ConnectTimeout != tt.wantConnect || got.Source.ReadTimeout != tt.wantRead {
			t.Errorf("parseArgs(%q): ConnectTimeout, ReadTimeout = %v, %v, want %v, %v",
				tt.args, got.Source.ConnectTimeout, got.Source.ReadTimeout, tt.wantConnect, tt.wantRead)
		}
	}
}

func TestParseArgsProgressInterval(t *testing.T) {
	for _, tt := range []struct {
		args []string
		want time.Duration
	}{
		{nil, 10 * time.Second},
		{[]string{"--progress-interval", "30s"}, 30 * time.Second},
		{[]string{"--progress-interval", "0"}, 0},
	} {
		got, err := parseArgs(append(tt.args, "-o", "out", "d"), noEnv)
		if err != nil {
			t.Fatalf("parseArgs(%q): %v", tt.args, err)
		}
		if got.ProgressInterval != tt.want {
			t.Errorf("parseArgs(%q): ProgressInterval = %v, want %v", tt.args, got.ProgressInterval, tt.want)
		}
	}
}

// --target mysql is the default and leaves the writer to dump.
func TestParseArgsTargetMySQL(t *testing.T) {
	got, err := parseArgs([]string{"--target", "mysql", "-o", "out", "d"}, noEnv)
	if err != nil {
		t.Fatalf("parseArgs: %v", err)
	}
	if got.Writer != nil {
		t.Errorf("Writer = %T, want nil so that dump builds the MySQL writer from Data", got.Writer)
	}
	if got.Data != (datafile.Options{RowsPerInsert: 1000, MaxInsertBytes: 1048576}) {
		t.Errorf("Data = %+v", got.Data)
	}
}

// --target sqlite sets the writer, which takes the INSERT options itself, so
// Data stays empty (dump rejects both).
func TestParseArgsTargetSQLite(t *testing.T) {
	got, err := parseArgs([]string{"--target", "sqlite", "--rows-per-insert", "7", "-o", "out", "d"}, noEnv)
	if err != nil {
		t.Fatalf("parseArgs: %v", err)
	}
	if _, ok := got.Writer.(*writer.SQLite); !ok {
		t.Fatalf("Writer = %T, want *writer.SQLite", got.Writer)
	}
	if got.Data != (datafile.Options{}) {
		t.Errorf("Data = %+v, want it empty next to a Writer", got.Data)
	}
}

func TestParseArgsTargetPostgres(t *testing.T) {
	got, err := parseArgs([]string{"--target", "postgres", "--rows-per-insert", "7", "-o", "out", "d"}, noEnv)
	if err != nil {
		t.Fatalf("parseArgs: %v", err)
	}
	if _, ok := got.Writer.(*writer.Postgres); !ok {
		t.Fatalf("Writer = %T, want *writer.Postgres", got.Writer)
	}
	if got.Data != (datafile.Options{}) {
		t.Errorf("Data = %+v, want it empty next to a Writer", got.Data)
	}
}

func TestParseArgsFlags(t *testing.T) {
	env := func(k string) string {
		if k == "MYSQL_PWD" {
			return "envpw"
		}
		return ""
	}
	for _, tt := range []struct {
		name string
		args []string
		want dump.Options
	}{
		{
			"short flags",
			[]string{"-h", "db.example", "-P", "3307", "-u", "alice", "-o", "out", "d"},
			dump.Options{
				Source: source.Config{Host: "db.example", Port: 3307, User: "alice", Password: "envpw", SSLMode: "preferred", Jobs: 1, LockTimeout: source.DefaultLockTimeout, ConnectTimeout: source.DefaultConnectTimeout},
				DBs:    []string{"d"}, OutDir: "out",
				Data: datafile.Options{RowsPerInsert: 1000, MaxInsertBytes: 1048576},
			},
		},
		{
			"long flags",
			[]string{"--host", "db.example", "--port", "3307", "--user", "alice", "--socket", "/tmp/s.sock",
				"--output", "out", "--rows-per-insert", "5", "--max-insert-bytes", "4096", "d"},
			dump.Options{
				Source: source.Config{Host: "db.example", Port: 3307, Socket: "/tmp/s.sock", User: "alice", Password: "envpw", SSLMode: "preferred", Jobs: 1, LockTimeout: source.DefaultLockTimeout, ConnectTimeout: source.DefaultConnectTimeout},
				DBs:    []string{"d"}, OutDir: "out",
				Data: datafile.Options{RowsPerInsert: 5, MaxInsertBytes: 4096},
			},
		},
		{
			"ssl flags",
			[]string{"--ssl-mode", "verify-ca", "--ssl-ca", "/etc/ca.pem", "-o", "out", "d"},
			dump.Options{
				Source: source.Config{Host: "127.0.0.1", Port: 3306, Password: "envpw", SSLMode: "verify-ca", SSLCA: "/etc/ca.pem", Jobs: 1, LockTimeout: source.DefaultLockTimeout, ConnectTimeout: source.DefaultConnectTimeout},
				DBs:    []string{"d"}, OutDir: "out",
				Data: datafile.Options{RowsPerInsert: 1000, MaxInsertBytes: 1048576},
			},
		},
		{
			"rows-per-file flag",
			[]string{"--rows-per-file", "50000", "-o", "out", "d"},
			dump.Options{
				Source: source.Config{Host: "127.0.0.1", Port: 3306, Password: "envpw", SSLMode: "preferred", Jobs: 1, LockTimeout: source.DefaultLockTimeout, ConnectTimeout: source.DefaultConnectTimeout},
				DBs:    []string{"d"}, OutDir: "out",
				Data: datafile.Options{RowsPerInsert: 1000, MaxInsertBytes: 1048576, RowsPerFile: 50000},
			},
		},
		{
			"mode flags",
			[]string{"--file-mode", "0640", "--dir-mode", "750", "-o", "out", "d"},
			dump.Options{
				Source: source.Config{Host: "127.0.0.1", Port: 3306, Password: "envpw", SSLMode: "preferred", Jobs: 1, LockTimeout: source.DefaultLockTimeout, ConnectTimeout: source.DefaultConnectTimeout},
				DBs:    []string{"d"}, OutDir: "out",
				Data:     datafile.Options{RowsPerInsert: 1000, MaxInsertBytes: 1048576},
				FileMode: 0o640, DirMode: 0o750,
			},
		},
		{
			"no-lock flag",
			[]string{"--no-lock", "-o", "out", "d"},
			dump.Options{
				Source: source.Config{Host: "127.0.0.1", Port: 3306, Password: "envpw", SSLMode: "preferred", Jobs: 1, LockTimeout: source.DefaultLockTimeout, ConnectTimeout: source.DefaultConnectTimeout, NoLock: true},
				DBs:    []string{"d"}, OutDir: "out",
				Data: datafile.Options{RowsPerInsert: 1000, MaxInsertBytes: 1048576},
			},
		},
		{
			"jobs flag",
			[]string{"--jobs", "4", "-o", "out", "d"},
			dump.Options{
				Source: source.Config{Host: "127.0.0.1", Port: 3306, Password: "envpw", SSLMode: "preferred", Jobs: 4, LockTimeout: source.DefaultLockTimeout, ConnectTimeout: source.DefaultConnectTimeout},
				DBs:    []string{"d"}, OutDir: "out",
				Data: datafile.Options{RowsPerInsert: 1000, MaxInsertBytes: 1048576},
			},
		},
		{
			"jobs short flag at the upper bound",
			[]string{"-j", "64", "-o", "out", "d"},
			dump.Options{
				Source: source.Config{Host: "127.0.0.1", Port: 3306, Password: "envpw", SSLMode: "preferred", Jobs: 64, LockTimeout: source.DefaultLockTimeout, ConnectTimeout: source.DefaultConnectTimeout},
				DBs:    []string{"d"}, OutDir: "out",
				Data: datafile.Options{RowsPerInsert: 1000, MaxInsertBytes: 1048576},
			},
		},
		{
			"one job with no-lock",
			[]string{"-j", "1", "--no-lock", "-o", "out", "d"},
			dump.Options{
				Source: source.Config{Host: "127.0.0.1", Port: 3306, Password: "envpw", SSLMode: "preferred", Jobs: 1, LockTimeout: source.DefaultLockTimeout, ConnectTimeout: source.DefaultConnectTimeout, NoLock: true},
				DBs:    []string{"d"}, OutDir: "out",
				Data: datafile.Options{RowsPerInsert: 1000, MaxInsertBytes: 1048576},
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseArgs(tt.args, env)
			if err != nil {
				t.Fatalf("parseArgs: %v", err)
			}
			// The logger is checked in logflags_test.go, the progress interval in
			// TestParseArgsProgressInterval.
			got.Log, got.ProgressInterval = nil, 0
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parseArgs = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestParseArgsSSLValidation(t *testing.T) {
	for _, args := range [][]string{
		{"--ssl-mode", "bogus"},
		{"--ssl-mode", "verify-ca"},
		{"--ssl-mode", "disabled", "--ssl-ca", "ca.pem"},
		{"--ssl-ca", "ca.pem"},
		{"--ssl-mode", "verify-identity", "--socket", "/tmp/s.sock"},
	} {
		if _, err := parseArgs(append(args, "-o", "out", "d"), noEnv); !errors.Is(err, errUsage) {
			t.Errorf("parseArgs(%q) error = %v, want usage error", args, err)
		}
	}
}

func TestParseArgsPasswordFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pw")
	if err := os.WriteFile(path, []byte("file pw\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := func(k string) string {
		if k == "MYSQL_PWD" {
			return "envpw"
		}
		return ""
	}
	got, err := parseArgs([]string{"--password-file", path, "-o", "out", "d"}, env)
	if err != nil {
		t.Fatalf("parseArgs: %v", err)
	}
	// The file wins over MYSQL_PWD; only the trailing line break is removed.
	if got.Source.Password != "file pw" {
		t.Errorf("Password = %q, want %q", got.Source.Password, "file pw")
	}
}

func TestParseArgsPasswordFileMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("mode bits carry no access rules on windows")
	}
	for _, tt := range []struct {
		mode os.FileMode
		warn bool
	}{
		{0o600, false},
		{0o400, false},
		{0o640, true},
		{0o604, true},
		{0o644, true},
	} {
		path := filepath.Join(t.TempDir(), "pw")
		if err := os.WriteFile(path, []byte("topsecret\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, tt.mode); err != nil {
			t.Fatal(err)
		}
		var stderr bytes.Buffer
		got, err := parseArgsTo([]string{"--password-file", path, "-o", "out", "d"}, noEnv, &stderr)
		if err != nil {
			t.Fatalf("mode %o: parseArgs: %v", tt.mode, err)
		}
		if got.Source.Password != "topsecret" {
			t.Errorf("mode %o: Password = %q, the run must go on", tt.mode, got.Source.Password)
		}
		out := stderr.String()
		if has := strings.Contains(out, "level=WARN"); has != tt.warn {
			t.Errorf("mode %o: warned = %v, want %v; stderr: %q", tt.mode, has, tt.warn, out)
		}
		if tt.warn && !strings.Contains(out, path) {
			t.Errorf("mode %o: warning does not name the file: %q", tt.mode, out)
		}
		if strings.Contains(out, "topsecret") {
			t.Errorf("mode %o: the password is in the log: %q", tt.mode, out)
		}
	}
}

func TestRunHelp(t *testing.T) {
	var stderr bytes.Buffer
	if code := run([]string{"-help"}, noEnv, io.Discard, &stderr); code != 0 {
		t.Errorf("run(-help) = %d, want 0", code)
	}
	if !strings.Contains(stderr.String(), "--password-file") {
		t.Errorf("usage does not mention --password-file:\n%s", stderr.String())
	}
	if !strings.Contains(stderr.String(), "--version") {
		t.Errorf("usage does not mention --version:\n%s", stderr.String())
	}
}

func TestRunVersion(t *testing.T) {
	oldVersion, oldCommit := version, commit
	t.Cleanup(func() { version, commit = oldVersion, oldCommit })
	version, commit = "1.2.3", "abc1234"

	for _, arg := range []string{"--version", "-version"} {
		// --version needs neither -o nor a database name. It is checked only
		// after the flags parse, so a malformed flag is still a usage error.
		var stdout, stderr bytes.Buffer
		if code := run([]string{arg}, noEnv, &stdout, &stderr); code != 0 {
			t.Errorf("run(%s) = %d, want 0; stderr: %s", arg, code, stderr.String())
		}
		if got, want := stdout.String(), "mysqldance 1.2.3 (commit abc1234)\n"; got != want {
			t.Errorf("run(%s) stdout = %q, want %q", arg, got, want)
		}
		if stderr.Len() != 0 {
			t.Errorf("run(%s) wrote to stderr: %q", arg, stderr.String())
		}
	}
}
