package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

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
		Source: source.Config{Host: "127.0.0.1", Port: 3306, SSLMode: "preferred"},
		DBs:    []string{"db1", "db2"},
		OutDir: "out",
		Data:   datafile.Options{RowsPerInsert: 1000, MaxInsertBytes: 1048576},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseArgs = %+v, want %+v", got, want)
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
				Source: source.Config{Host: "db.example", Port: 3307, User: "alice", Password: "envpw", SSLMode: "preferred"},
				DBs:    []string{"d"}, OutDir: "out",
				Data: datafile.Options{RowsPerInsert: 1000, MaxInsertBytes: 1048576},
			},
		},
		{
			"long flags",
			[]string{"--host", "db.example", "--port", "3307", "--user", "alice", "--socket", "/tmp/s.sock",
				"--output", "out", "--rows-per-insert", "5", "--max-insert-bytes", "4096", "d"},
			dump.Options{
				Source: source.Config{Host: "db.example", Port: 3307, Socket: "/tmp/s.sock", User: "alice", Password: "envpw", SSLMode: "preferred"},
				DBs:    []string{"d"}, OutDir: "out",
				Data: datafile.Options{RowsPerInsert: 5, MaxInsertBytes: 4096},
			},
		},
		{
			"ssl flags",
			[]string{"--ssl-mode", "verify-ca", "--ssl-ca", "/etc/ca.pem", "-o", "out", "d"},
			dump.Options{
				Source: source.Config{Host: "127.0.0.1", Port: 3306, Password: "envpw", SSLMode: "verify-ca", SSLCA: "/etc/ca.pem"},
				DBs:    []string{"d"}, OutDir: "out",
				Data: datafile.Options{RowsPerInsert: 1000, MaxInsertBytes: 1048576},
			},
		},
		{
			"rows-per-file flag",
			[]string{"--rows-per-file", "50000", "-o", "out", "d"},
			dump.Options{
				Source: source.Config{Host: "127.0.0.1", Port: 3306, Password: "envpw", SSLMode: "preferred"},
				DBs:    []string{"d"}, OutDir: "out",
				Data: datafile.Options{RowsPerInsert: 1000, MaxInsertBytes: 1048576, RowsPerFile: 50000},
			},
		},
		{
			"mode flags",
			[]string{"--file-mode", "0640", "--dir-mode", "750", "-o", "out", "d"},
			dump.Options{
				Source: source.Config{Host: "127.0.0.1", Port: 3306, Password: "envpw", SSLMode: "preferred"},
				DBs:    []string{"d"}, OutDir: "out",
				Data:     datafile.Options{RowsPerInsert: 1000, MaxInsertBytes: 1048576},
				FileMode: 0o640, DirMode: 0o750,
			},
		},
		{
			"no-lock flag",
			[]string{"--no-lock", "-o", "out", "d"},
			dump.Options{
				Source: source.Config{Host: "127.0.0.1", Port: 3306, Password: "envpw", SSLMode: "preferred", NoLock: true},
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
