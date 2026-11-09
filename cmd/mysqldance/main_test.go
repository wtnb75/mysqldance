package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"mysqldance/internal/datafile"
	"mysqldance/internal/dump"
	"mysqldance/internal/source"
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
		{"zero max insert bytes", []string{"--max-insert-bytes", "0", "-o", t.TempDir(), "db1"}},
		{"unknown ssl mode", []string{"--ssl-mode", "bogus", "-o", t.TempDir(), "db1"}},
		{"verify-ca without ssl-ca", []string{"--ssl-mode", "verify-ca", "-o", t.TempDir(), "db1"}},
		{"ssl-ca with default mode", []string{"--ssl-ca", "ca.pem", "-o", t.TempDir(), "db1"}},
		{"ssl-ca with disabled", []string{"--ssl-mode", "disabled", "--ssl-ca", "ca.pem", "-o", t.TempDir(), "db1"}},
		{"missing password file", []string{"--password-file", filepath.Join(t.TempDir(), "none"), "-o", t.TempDir(), "db1"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var stderr bytes.Buffer
			if code := run(tt.args, noEnv, &stderr); code != 1 {
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
	if code := run([]string{"-help"}, noEnv, &stderr); code != 0 {
		t.Errorf("run(-help) = %d, want 0", code)
	}
	if !strings.Contains(stderr.String(), "--password-file") {
		t.Errorf("usage does not mention --password-file:\n%s", stderr.String())
	}
}
