// Command mysqldance dumps MySQL databases into deterministic SQL files.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"mysqldance/internal/datafile"
	"mysqldance/internal/dump"
	"mysqldance/internal/source"
	"mysqldance/internal/writer"
)

// version and commit are set at build time by goreleaser through
// -ldflags "-X main.version=... -X main.commit=...".
var (
	version = "dev"
	commit  = "unknown"
)

func main() {
	os.Exit(run(os.Args[1:], os.Getenv, os.Stdout, os.Stderr))
}

// The values of --target: the DBMS the output is written for.
const (
	targetMySQL    = "mysql"
	targetSQLite   = "sqlite"
	targetPostgres = "postgres"
)

// errUsage marks errors already reported by the flag package.
var errUsage = errors.New("usage error")

// errVersion reports that --version was given and nothing else must run.
var errVersion = errors.New("version requested")

// run executes the command and returns the process exit code.
func run(args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	opt, err := parseArgsTo(args, getenv, stderr)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if errors.Is(err, errVersion) {
		_, _ = fmt.Fprintf(stdout, "mysqldance %s (commit %s)\n", version, commit)
		return 0
	}
	if err != nil {
		if !errors.Is(err, errUsage) {
			_, _ = fmt.Fprintf(stderr, "mysqldance: %v\n", err)
		}
		return 1
	}
	ctx, stop := notifyContext(context.Background())
	defer stop()
	if err := dump.Run(ctx, opt); err != nil {
		// Report through the logger so --log-format and --log-level apply.
		opt.Log.Error("dump failed", "error", err)
		return 1
	}
	return 0
}

// notifyContext returns a context canceled by Ctrl-C (SIGINT) or SIGTERM, the
// signal sent by docker stop and kill, so both take the same cleanup path.
// Once the first signal has canceled the context, the signals go back to
// their default action, so a second one terminates the process even if the
// cleanup is stuck.
func notifyContext(parent context.Context) (context.Context, context.CancelFunc) {
	ctx, stop := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-ctx.Done()
		stop()
	}()
	return ctx, stop
}

// newLogger returns a logger that writes to w (standard error), so that
// standard output stays free of anything but --version.
func newLogger(w io.Writer, level, format string) (*slog.Logger, error) {
	var lv slog.Level
	switch level {
	case "debug":
		lv = slog.LevelDebug
	case "info":
		lv = slog.LevelInfo
	case "warn":
		lv = slog.LevelWarn
	case "error":
		lv = slog.LevelError
	default:
		return nil, fmt.Errorf("--log-level must be debug, info, warn or error, not %q", level)
	}
	h := &slog.HandlerOptions{Level: lv}
	switch format {
	case "text":
		return slog.New(slog.NewTextHandler(w, h)), nil
	case "json":
		return slog.New(slog.NewJSONHandler(w, h)), nil
	}
	return nil, fmt.Errorf("--log-format must be text or json, not %q", format)
}

// parseMode parses an octal permission such as "0640". An empty s means the
// default (0). Only the nine permission bits are accepted, and the owner must
// have at least the bits in ownerNeeds (a directory must stay usable by the
// tool, a file readable by its owner).
func parseMode(s string, ownerNeeds os.FileMode) (os.FileMode, error) {
	if s == "" {
		return 0, nil
	}
	v, err := strconv.ParseUint(s, 8, 32)
	if err != nil || v > 0o777 {
		return 0, fmt.Errorf("%q is not an octal permission between 0 and 0777", s)
	}
	m := os.FileMode(v)
	if m&ownerNeeds != ownerNeeds {
		return 0, fmt.Errorf("%q must give the owner at least %04o", s, ownerNeeds)
	}
	return m, nil
}

// parseArgs parses the command line into dump options without connecting.
func parseArgs(args []string, getenv func(string) string) (dump.Options, error) {
	return parseArgsTo(args, getenv, io.Discard)
}

func parseArgsTo(args []string, getenv func(string) string, stderr io.Writer) (dump.Options, error) {
	fs := flag.NewFlagSet("mysqldance", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprint(stderr, `usage: mysqldance [flags] -o DIR DB [DB...]

Flags must come before the database names.
The password is read from --password-file, else from MYSQL_PWD.

  -h, --host HOST            server host (default 127.0.0.1)
  -P, --port PORT            server port (default 3306)
  -u, --user USER            user name
      --socket PATH          Unix socket (overrides host and port)
      --password-file FILE   file holding the password
      --ssl-mode MODE        disabled, preferred, required, verify-ca or
                             verify-identity (default preferred)
      --ssl-ca FILE          PEM file of CA certificates to verify the server
                             (with required, verify-ca or verify-identity)
  -o, --output DIR           output directory (required)
      --target DBMS          mysql, sqlite or postgres: the DBMS the files are
                             written for (default mysql). sqlite and postgres
                             write tables, indexes and data, not views; see the
                             README
      --rows-per-insert N    average rows per INSERT statement (default 1000)
      --max-insert-bytes N   size cap of one INSERT statement (default 1048576)
      --rows-per-file N      split a table into files of N rows on average,
                             named TABLE.0001.sql, ... (default 0 = no split)
      --file-mode MODE       exact octal permission of the output files, e.g.
                             0640 (default 0666 filtered by the umask)
      --dir-mode MODE        exact octal permission of the output directory
                             when it is created, e.g. 0750 (default 0755
                             filtered by the umask; an existing one is kept)
      --no-lock              do not take FLUSH TABLES WITH READ LOCK when a
                             table is not InnoDB (warn instead)
      --lock-timeout DUR     give up when FLUSH TABLES WITH READ LOCK has not
                             got the lock after DUR, e.g. 30s or 2m (default
                             30s; 0 = the server's own lock_wait_timeout)
      --connect-timeout DUR  give up when the server cannot be reached within DUR
                             (default 30s; 0 = the operating system's own limit)
      --read-timeout DUR     give up when the server has not answered for DUR
                             (default 0 = no limit). It also covers a statement
                             that sends nothing until it is done, such as the
                             wait for the lock (see --lock-timeout) or a sort of
                             a large table, so keep it generous
  -j, --jobs N               read N tables in parallel on N connections that
                             show the same point in time (default 1). Above 1
                             the lock is always taken, so the RELOAD privilege
                             is needed, and --no-lock cannot be used
      --log-level LEVEL      debug, info, warn or error (default info). Logs go
                             to standard error
      --log-format FORMAT    text or json (default text)
      --version              print the version and commit, then exit
`)
	}

	var (
		host, user, socket, passwordFile, output string
		sslMode, sslCA, target                   string
		logLevel, logFormat                      string
		noLock, showVersion                      bool
		fileMode, dirMode                        string
		port, jobs                               int
		rowsPerInsert, rowsPerFile               uint64
		maxInsertBytes                           int
		lockTimeout                              time.Duration
		connectTimeout, readTimeout              time.Duration
	)
	fs.StringVar(&host, "h", "127.0.0.1", "")
	fs.StringVar(&host, "host", "127.0.0.1", "")
	fs.IntVar(&port, "P", 3306, "")
	fs.IntVar(&port, "port", 3306, "")
	fs.StringVar(&user, "u", "", "")
	fs.StringVar(&user, "user", "", "")
	fs.StringVar(&socket, "socket", "", "")
	fs.StringVar(&passwordFile, "password-file", "", "")
	fs.StringVar(&sslMode, "ssl-mode", source.SSLPreferred, "")
	fs.StringVar(&sslCA, "ssl-ca", "", "")
	fs.StringVar(&output, "o", "", "")
	fs.StringVar(&output, "output", "", "")
	fs.StringVar(&target, "target", targetMySQL, "")
	fs.Uint64Var(&rowsPerInsert, "rows-per-insert", 1000, "")
	fs.IntVar(&maxInsertBytes, "max-insert-bytes", 1048576, "")
	fs.Uint64Var(&rowsPerFile, "rows-per-file", 0, "")
	fs.BoolVar(&noLock, "no-lock", false, "")
	// A limit by default is intended (B-057): an unbounded wait for the lock
	// stalls every statement queued behind it. It applies only when a lock is taken.
	fs.DurationVar(&lockTimeout, "lock-timeout", source.DefaultLockTimeout, "")
	fs.DurationVar(&connectTimeout, "connect-timeout", source.DefaultConnectTimeout, "")
	// No limit by default: a read timeout would cut off the normal read of a
	// large table (it also covers statements that send nothing until done). It is
	// deliberately not checked against --lock-timeout: the README says to keep it higher.
	fs.DurationVar(&readTimeout, "read-timeout", 0, "")
	fs.IntVar(&jobs, "j", 1, "")
	fs.IntVar(&jobs, "jobs", 1, "")
	fs.StringVar(&logLevel, "log-level", "info", "")
	fs.StringVar(&logFormat, "log-format", "text", "")
	fs.BoolVar(&showVersion, "version", false, "")
	fs.StringVar(&fileMode, "file-mode", "", "")
	fs.StringVar(&dirMode, "dir-mode", "", "")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return dump.Options{}, err
		}
		return dump.Options{}, errUsage
	}
	if showVersion {
		return dump.Options{}, errVersion
	}
	usage := func(format string, a ...any) (dump.Options, error) {
		_, _ = fmt.Fprintf(stderr, "mysqldance: "+format+"\n", a...)
		fs.Usage()
		return dump.Options{}, errUsage
	}
	switch {
	case output == "":
		return usage("-o/--output is required")
	case target != targetMySQL && target != targetSQLite && target != targetPostgres:
		return usage("--target must be %s, %s or %s, not %q", targetMySQL, targetSQLite, targetPostgres, target)
	case fs.NArg() == 0:
		return usage("at least one database name is required")
	case rowsPerInsert == 0 || rowsPerInsert > math.MaxUint32:
		return usage("--rows-per-insert must be between 1 and %d", uint64(math.MaxUint32))
	case rowsPerFile > math.MaxUint32:
		return usage("--rows-per-file must be between 0 and %d", uint64(math.MaxUint32))
	case maxInsertBytes <= 0:
		return usage("--max-insert-bytes must be positive")
	case port <= 0 || port > 65535:
		return usage("--port must be between 1 and 65535")
	case jobs < 1 || jobs > source.MaxJobs:
		return usage("--jobs must be between 1 and %d", source.MaxJobs)
	case lockTimeout < 0:
		return usage("--lock-timeout must not be negative")
	case connectTimeout < 0:
		return usage("--connect-timeout must not be negative")
	case readTimeout < 0:
		return usage("--read-timeout must not be negative")
	case jobs > 1 && noLock:
		// Without the lock the snapshots of the connections cannot be aligned.
		return usage("--jobs above 1 cannot be combined with --no-lock: the lock is what makes " +
			"the connections show the same point in time")
	}
	if err := (source.Config{Socket: socket, SSLMode: sslMode, SSLCA: sslCA}).ValidateSSL(); err != nil {
		return usage("%v", err)
	}

	logger, err := newLogger(stderr, logLevel, logFormat)
	if err != nil {
		return usage("%v", err)
	}
	fm, err := parseMode(fileMode, 0o400)
	if err != nil {
		return usage("--file-mode: %v", err)
	}
	dm, err := parseMode(dirMode, 0o700)
	if err != nil {
		return usage("--dir-mode: %v", err)
	}

	data := datafile.Options{RowsPerInsert: uint32(rowsPerInsert), MaxInsertBytes: maxInsertBytes, RowsPerFile: uint32(rowsPerFile)}
	opt := dump.Options{
		Log:      logger,
		FileMode: fm,
		DirMode:  dm,
		DBs:      fs.Args(),
		OutDir:   output,
	}
	// dump builds the MySQL writer from Data; another writer takes the options
	// itself, and dump refuses both.
	switch target {
	case targetSQLite:
		opt.Writer = writer.NewSQLite(data)
	case targetPostgres:
		opt.Writer = writer.NewPostgres(data)
	default:
		opt.Data = data
	}
	opt.Source.Host, opt.Source.Port, opt.Source.Socket, opt.Source.User = host, port, socket, user
	opt.Source.SSLMode, opt.Source.SSLCA = sslMode, sslCA
	opt.Source.NoLock = noLock
	opt.Source.Jobs = jobs
	opt.Source.LockTimeout = lockTimeout
	opt.Source.ConnectTimeout, opt.Source.ReadTimeout = connectTimeout, readTimeout
	opt.Source.Password = getenv("MYSQL_PWD")
	if passwordFile != "" {
		b, err := os.ReadFile(passwordFile)
		if err != nil {
			return dump.Options{}, fmt.Errorf("read password file: %w", err)
		}
		pw := strings.TrimSuffix(string(b), "\n")
		opt.Source.Password = strings.TrimSuffix(pw, "\r")
	}
	return opt, nil
}
