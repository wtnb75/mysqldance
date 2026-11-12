package dump

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"reflect"
	"strings"
	"testing"
)

// Tests of the connection, snapshot and lock messages (B-088).

// connLogger is testLogger at the Debug level.
func connLogger(w io.Writer) *slog.Logger { return testLoggerAt(w, slog.LevelDebug) }

// logLine returns the first line of log that contains every one of parts, or "".
func logLine(log string, parts ...string) string {
	for line := range strings.SplitSeq(log, "\n") {
		ok := true
		for _, p := range parts {
			ok = ok && strings.Contains(line, p)
		}
		if ok {
			return line
		}
	}
	return ""
}

// runLogged dumps db with cfg at the Debug level and returns the log.
func (e *env) runLogged(t *testing.T, jobs int, noLock bool, db string) string {
	t.Helper()
	cfg := e.cfg
	cfg.Jobs, cfg.NoLock = jobs, noLock
	var log bytes.Buffer
	opt := Options{Source: cfg, DBs: []string{db}, OutDir: t.TempDir(), Log: connLogger(&log)}
	if err := Run(context.Background(), opt); err != nil {
		t.Fatalf("Run(jobs=%d, noLock=%v): %v\n%s", jobs, noLock, err, log.String())
	}
	return log.String()
}

// requireNoSecret fails when the log shows the password or a connection string.
func (e *env) requireNoSecret(t *testing.T, log string) {
	t.Helper()
	if e.cfg.Password != "" && strings.Contains(log, e.cfg.Password) {
		t.Errorf("log contains the password:\n%s", log)
	}
	for _, s := range []string{"password", "passwd", "dsn", "@tcp("} {
		if strings.Contains(strings.ToLower(log), s) {
			t.Errorf("log contains %q:\n%s", s, log)
		}
	}
}

func (e *env) requireTarget(t *testing.T, log string) {
	t.Helper()
	var want string
	if e.cfg.Socket != "" {
		want = "socket=" + e.cfg.Socket
	} else {
		want = "host=" + e.cfg.Host
	}
	if logLine(log, "level=INFO", "connected", want) == "" {
		t.Errorf("no Info line \"connected\" with %s:\n%s", want, log)
	}
}

func TestConnectionLogWithoutLock(t *testing.T) {
	e := newEnv(t)
	db := uniqueName(t)
	e.createDB(t, db)
	e.exec(t, "CREATE TABLE t (id INT PRIMARY KEY)", "INSERT INTO t VALUES (1)")

	log := e.runLogged(t, 1, false, db)
	e.requireTarget(t, log)
	e.requireNoSecret(t, log)
	if logLine(log, "level=INFO", "connected", "jobs=1") == "" {
		t.Errorf("no Info line \"connected\" with jobs=1:\n%s", log)
	}
	if logLine(log, "level=INFO", "consistent snapshot started", "global_lock=false") == "" {
		t.Errorf("no Info line \"consistent snapshot started\" with global_lock=false:\n%s", log)
	}
	if strings.Contains(log, "global read lock") || strings.Contains(log, "FLUSH TABLES") {
		t.Errorf("log mentions the lock although none was taken:\n%s", log)
	}
	if logLine(log, "level=DEBUG", "statement=\"START TRANSACTION WITH CONSISTENT SNAPSHOT\"") == "" {
		t.Errorf("no Debug line for START TRANSACTION:\n%s", log)
	}
}

func TestConnectionLogWithLock(t *testing.T) {
	e := newEnv(t)
	db := uniqueName(t)
	e.createDB(t, db)
	e.exec(t, "CREATE TABLE my (id INT PRIMARY KEY) ENGINE=MyISAM", "INSERT INTO my VALUES (1)")

	log := e.runLogged(t, 1, false, db)
	e.requireTarget(t, log)
	e.requireNoSecret(t, log)
	// The lock is kept until Close, so "released" is the last of the three.
	order := []string{"global read lock acquired", "consistent snapshot started", "global read lock released"}
	last := -1
	for _, msg := range order {
		line := logLine(log, "level=INFO", msg)
		if line == "" {
			t.Fatalf("no Info line %q:\n%s", msg, log)
		}
		i := strings.Index(log, line)
		if i < last {
			t.Errorf("%q is out of order:\n%s", msg, log)
		}
		last = i
	}
	for _, msg := range []string{"global read lock acquired", "global read lock released"} {
		if logLine(log, msg, "duration=") == "" {
			t.Errorf("line %q has no duration attribute:\n%s", msg, log)
		}
	}
	if logLine(log, "consistent snapshot started", "global_lock=true") == "" {
		t.Errorf("snapshot line does not say the lock is held:\n%s", log)
	}
	if logLine(log, "level=DEBUG", "statement=\"FLUSH TABLES WITH READ LOCK\"") == "" {
		t.Errorf("no Debug line for FLUSH TABLES WITH READ LOCK:\n%s", log)
	}
}

func TestConnectionLogParallel(t *testing.T) {
	e := newEnv(t)
	db := e.threeTables(t)

	// InnoDB only: the lock is taken, and released as soon as the snapshots
	// have started.
	log := e.runLogged(t, 3, false, db)
	e.requireTarget(t, log)
	e.requireNoSecret(t, log)
	if logLine(log, "level=INFO", "connected", "jobs=3") == "" {
		t.Errorf("no Info line \"connected\" with jobs=3:\n%s", log)
	}
	acquired := strings.Index(log, "global read lock acquired")
	released := strings.Index(log, "global read lock released")
	started := strings.Index(log, "consistent snapshot started")
	if acquired < 0 || released < acquired || started < released {
		t.Errorf("want acquired, released, snapshot started in this order:\n%s", log)
	}
	if logLine(log, "consistent snapshot started", "global_lock=false", "jobs=3") == "" {
		t.Errorf("snapshot line should say the lock was released:\n%s", log)
	}
	if logLine(log, "level=DEBUG", "statement=\"UNLOCK TABLES\"") == "" {
		t.Errorf("no Debug line for UNLOCK TABLES:\n%s", log)
	}

	// A non-InnoDB table keeps the lock until Close.
	e.exec(t, "CREATE TABLE m (id INT PRIMARY KEY) ENGINE=MyISAM")
	log = e.runLogged(t, 3, false, db)
	if logLine(log, "consistent snapshot started", "global_lock=true", "jobs=3") == "" {
		t.Errorf("snapshot line should say the lock is kept:\n%s", log)
	}
	if strings.Index(log, "global read lock released") < strings.Index(log, "consistent snapshot started") {
		t.Errorf("the lock should be released after the snapshot started:\n%s", log)
	}
}

// TestConnectionLogDoesNotChangeOutput checks that the files do not depend on
// the logger or its level.
func TestConnectionLogDoesNotChangeOutput(t *testing.T) {
	e := newEnv(t)
	db := e.threeTables(t)
	e.exec(t, "CREATE TABLE m (id INT PRIMARY KEY) ENGINE=MyISAM")

	var out [3]map[string]string
	for i, lg := range []*slog.Logger{nil, testLogger(io.Discard), connLogger(io.Discard)} {
		dir := t.TempDir()
		if err := Run(context.Background(), Options{Source: e.cfg, DBs: []string{db}, OutDir: dir, Log: lg}); err != nil {
			t.Fatal(err)
		}
		out[i] = readFiles(t, dir)
	}
	if !reflect.DeepEqual(out[0], out[1]) || !reflect.DeepEqual(out[0], out[2]) {
		t.Errorf("files differ with the logger: %s / %s", diffNames(out[0], out[1]), diffNames(out[0], out[2]))
	}
}
