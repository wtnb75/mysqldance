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
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"mysqldance/internal/datafile"
	"mysqldance/internal/dump"
	"mysqldance/internal/mask"
	"mysqldance/internal/source"
	"mysqldance/internal/writer"
)

// version and commit are set at build time by goreleaser through
// -ldflags "-X main.version=... -X main.commit=...". Left at the defaults,
// --version falls back to the build info the Go toolchain recorded (see
// resolveBuildInfo).
var (
	version = defaultVersion
	commit  = defaultCommit
)

func main() {
	os.Exit(run(os.Args[1:], os.Getenv, os.Stdout, os.Stderr))
}

// readPasswordFile reads the file and warns when group or others can read it.
// The mode is taken from the opened file, so it is the one of the bytes read.
// Windows mode bits carry no access rules, so nothing is checked there. The
// warning never carries the contents.
func readPasswordFile(path string, logger *slog.Logger) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	if runtime.GOOS != "windows" {
		// Read bits only (B-024); a failed Stat skips the check, the read below still runs.
		if fi, err := f.Stat(); err == nil {
			if perm := fi.Mode().Perm(); perm&0o044 != 0 {
				logger.Warn(fmt.Sprintf("password file %q is readable by group or others (mode %04o); "+
					"restrict it, for example with chmod 600", path, perm))
			}
		}
	}
	return io.ReadAll(f)
}

// The values of --definer: what to do with the DEFINER clause.
const (
	definerStrip = "strip"
	definerKeep  = "keep"
)

// The values of --sql-security: what to do with SQL SECURITY of views and
// routines.
const (
	sqlSecurityKeep    = "keep"
	sqlSecurityInvoker = "invoker"
)

// The values of --target: the DBMS the output is written for.
const (
	targetMySQL    = "mysql"
	targetSQLite   = "sqlite"
	targetPostgres = "postgres"
)

// The values of --progress.
const (
	progressAuto = "auto"
	progressBar  = "bar"
	progressLog  = "log"
	progressNone = "none"
)

// errUsage marks errors already reported by the flag package.
var errUsage = errors.New("usage error")

// errVersion reports that --version was given and nothing else must run.
var errVersion = errors.New("version requested")

// run executes the command and returns the process exit code.
func run(args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	// Only an exact first argument is a subcommand; anything else is the dump.
	if len(args) > 0 && args[0] == subcommandScanK {
		return runScanK(args[1:], getenv, stdout, stderr)
	}
	opt, err := parseArgsTo(args, getenv, stderr)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if errors.Is(err, errVersion) {
		v, c := buildVersion()
		_, _ = fmt.Fprintf(stdout, "mysqldance %s (commit %s)\n", v, c)
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

// minSortMemory is the smallest --sort-memory: a smaller one would write a
// scratch file for a few hundred rows.
const minSortMemory = 1 << 20

// parseSize parses a size in bytes: digits, optionally followed by a binary
// unit (K, KB or KiB for 1024 bytes, then M and G likewise), in any case.
func parseSize(s string) (int64, error) {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i == 0 {
		return 0, fmt.Errorf("%q is not a size such as 64MiB", s)
	}
	n, err := strconv.ParseInt(s[:i], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%q is too large", s)
	}
	var mult int64
	switch strings.ToLower(s[i:]) {
	case "", "b":
		mult = 1
	case "k", "kb", "kib":
		mult = 1 << 10
	case "m", "mb", "mib":
		mult = 1 << 20
	case "g", "gb", "gib":
		mult = 1 << 30
	default:
		return 0, fmt.Errorf("%q has an unknown unit (use KiB, MiB or GiB)", s)
	}
	if n > math.MaxInt64/mult {
		return 0, fmt.Errorf("%q is too large", s)
	}
	return n * mult, nil
}

// stringList is a flag that may be given more than once; the values keep the
// order given.
type stringList []string

func (l *stringList) String() string { return strings.Join(*l, ",") }

func (l *stringList) Set(v string) error {
	*l = append(*l, v)
	return nil
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
Subcommand: mysqldance scan-k --help (measure k-anonymity of the columns marked
quasi in the rule file). It counts only as the first argument.

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
      --definer MODE         strip (default) or keep. strip removes the DEFINER
                             clause of views, triggers, routines and events, so
                             they belong to the user who restores; keep writes
                             it as the server returned it, and the restore
                             fails (error 1449) unless that user exists. No
                             effect with --target sqlite or postgres
      --sql-security MODE    keep (default) or invoker. keep writes SQL SECURITY
                             of views and routines as the server returned it;
                             invoker makes them SQL SECURITY INVOKER, so they
                             run with the privileges of the caller, not of the
                             definer. It changes what a view shows and what a
                             routine may do. No effect with --target sqlite or
                             postgres
      --pg-schema DB=SCHEMA  with --target postgres, name the schema a database
                             becomes (default: the database's own name). For a
                             database whose name starts with pg_, which
                             PostgreSQL reserves. Repeatable; SCHEMA is at most
                             63 bytes and does not start with pg_. File names
                             keep the database name
      --case-safe-names      also escape the upper-case letters of database and
                             table names in file names (A -> %41), so names that
                             differ only in case, such as Users and users, do not
                             collide. Changes the file names that have an
                             upper-case letter in them
      --rows-per-insert N    average rows per INSERT statement (default 1000)
      --max-insert-bytes N   size cap of one INSERT statement (default 1048576)
      --rows-per-file N      split a table into files of N rows on average,
                             named TABLE.0001.sql, ... (default 0 = no split)
      --file-mode MODE       exact octal permission of the output files, e.g.
                             0640 (default 0666 filtered by the umask)
      --dir-mode MODE        exact octal permission of the output directory
                             when it is created, e.g. 0750 (default 0755
                             filtered by the umask; an existing one is kept)
      --exclude-table DB.TABLE
                             leave a table out of the output: its schema, data
                             and triggers. Repeatable; exact names, split at the
                             first dot. It must be a base table of a database to
                             dump
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
                             (on MySQL 8.0 and later, FLUSH_TABLES also works)
                             is needed, and --no-lock cannot be used
      --sort-memory SIZE     about how much memory a table without a key may use
                             while it is sorted, e.g. 64MiB or 1GiB (default
                             64MiB, at least 1MiB); past that the sort goes
                             through temporary files in the output directory.
                             With --jobs N, N tables are sorted at once
      --progress MODE        how to show the progress while the tables are read:
                             bar (one line redrawn with \r, 4 times a second),
                             log ("dump progress" records), none, or auto
                             (default: bar when standard error is a terminal,
                             else log). bar also works when it is not a
                             terminal; the line then holds \r characters
      --progress-interval DUR
                             log a "dump progress" record (rows, bytes, speed,
                             estimated remaining time) every DUR while the
                             tables are read, e.g. 30s or 1m (default 10s;
                             0 = none). It applies to --progress log only
      --mask-rules FILE      YAML file of rules that transform column values as
                             they are read, so the output holds only the
                             transformed rows (transforms: fixed, nullify,
                             keep). Checked against the databases before the
                             output directory is touched; see the README
      --strict-columns       make a column with no rule an error: every output
                             column needs a rule (use: keep if it needs no
                             masking) or a DB.TABLE.* rule of keep for its
                             table. Needs --mask-rules
      --salt-file FILE       file holding the salt for the transforms that need
                             one (at least 16 bytes; group and others must not
                             be able to read or write it), else MYSQLDANCE_SALT.
                             Needs --mask-rules. Checked whenever it is given
      --log-level LEVEL      debug, info, warn or error (default info). Logs go
                             to standard error
      --log-format FORMAT    text or json (default text)
      --version              print the version and commit, then exit
`)
	}

	var excludeTables, pgSchemas stringList
	var (
		host, user, socket, passwordFile, output string
		sslMode, sslCA, target, definer          string
		sqlSecurity                              string
		maskRules, saltFile                      string
		logLevel, logFormat                      string
		noLock, showVersion, caseSafeNames       bool
		strictColumns                            bool
		fileMode, dirMode                        string
		sortMemory                               string
		port, jobs                               int
		rowsPerInsert, rowsPerFile               uint64
		maxInsertBytes                           int
		lockTimeout                              time.Duration
		connectTimeout, readTimeout              time.Duration
		progressInterval                         time.Duration
		progressMode                             string
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
	fs.StringVar(&definer, "definer", definerStrip, "")
	fs.StringVar(&sqlSecurity, "sql-security", sqlSecurityKeep, "")
	fs.BoolVar(&caseSafeNames, "case-safe-names", false, "")
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
	fs.DurationVar(&progressInterval, "progress-interval", dump.DefaultProgressInterval, "")
	fs.StringVar(&progressMode, "progress", progressAuto, "")
	fs.IntVar(&jobs, "j", 1, "")
	fs.IntVar(&jobs, "jobs", 1, "")
	fs.StringVar(&maskRules, "mask-rules", "", "")
	fs.StringVar(&saltFile, "salt-file", "", "")
	fs.BoolVar(&strictColumns, "strict-columns", false, "")
	fs.StringVar(&logLevel, "log-level", "info", "")
	fs.StringVar(&logFormat, "log-format", "text", "")
	fs.BoolVar(&showVersion, "version", false, "")
	fs.StringVar(&fileMode, "file-mode", "", "")
	fs.StringVar(&dirMode, "dir-mode", "", "")
	fs.StringVar(&sortMemory, "sort-memory", "", "")
	fs.Var(&excludeTables, "exclude-table", "")
	fs.Var(&pgSchemas, "pg-schema", "")

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
	case definer != definerStrip && definer != definerKeep:
		return usage("--definer must be %s or %s, not %q", definerStrip, definerKeep, definer)
	case sqlSecurity != sqlSecurityKeep && sqlSecurity != sqlSecurityInvoker:
		return usage("--sql-security must be %s or %s, not %q", sqlSecurityKeep, sqlSecurityInvoker, sqlSecurity)
	case fs.NArg() == 0:
		return usage("at least one database name is required")
	case saltFile != "" && maskRules == "":
		// A salt with no rules would be read and dropped, and the run would
		// look masked when nothing was.
		return usage("--salt-file needs --mask-rules")
	case strictColumns && maskRules == "":
		// Without rules every column would be undecided; asking for the check
		// with nothing to check is a mistake, not a request to refuse the dump.
		return usage("--strict-columns needs --mask-rules")
	case len(pgSchemas) > 0 && target != targetPostgres:
		return usage("--pg-schema applies to --target %s only", targetPostgres)
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
	case progressInterval < 0:
		return usage("--progress-interval must not be negative")
	case !slices.Contains([]string{progressAuto, progressBar, progressLog, progressNone}, progressMode):
		return usage("--progress must be %s, %s, %s or %s, not %q",
			progressAuto, progressBar, progressLog, progressNone, progressMode)
	case jobs > 1 && noLock:
		// Without the lock the snapshots of the connections cannot be aligned.
		return usage("--jobs above 1 cannot be combined with --no-lock: the lock is what makes " +
			"the connections show the same point in time")
	}
	if err := (source.Config{Socket: socket, SSLMode: sslMode, SSLCA: sslCA}).ValidateSSL(); err != nil {
		return usage("%v", err)
	}

	// The bar and the log share standard error: the logger writes through the bar,
	// which takes the bar off the screen for each record (B-203).
	var bar *dump.ProgressBar
	logOut := stderr
	isTTY, _ := terminalOf(stderr)
	if progressMode == progressBar || (progressMode == progressAuto && isTTY) {
		bar = dump.NewProgressBar(stderr, func() int {
			_, cols := terminalOf(stderr)
			return columnsOf(cols, getenv)
		})
		logOut = bar
		// The bar shows what the records would, so there are none even when
		// --progress-interval is given; --progress log asks for them (R-212).
		progressInterval = 0
	} else if progressMode == progressNone {
		progressInterval = 0
	}
	logger, err := newLogger(logOut, logLevel, logFormat)
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

	for _, spec := range excludeTables {
		if _, _, err := source.SplitTableSpec(spec); err != nil {
			return usage("--exclude-table: %v", err)
		}
	}

	schemaNames, err := writer.ParsePGSchemas(pgSchemas, fs.Args())
	if err != nil {
		return usage("--pg-schema: %v", err)
	}

	// Unset (empty) leaves 0, which dump reads as its default. An explicit empty
	// value counts as unset, as with --file-mode and --dir-mode (review ledger R-165).
	var sortBytes int64
	if sortMemory != "" {
		n, err := parseSize(sortMemory)
		switch {
		case err != nil:
			return usage("--sort-memory: %v", err)
		case n < minSortMemory:
			return usage("--sort-memory must be at least 1MiB")
		}
		sortBytes = n
	}

	data := datafile.Options{RowsPerInsert: uint32(rowsPerInsert), MaxInsertBytes: maxInsertBytes, RowsPerFile: uint32(rowsPerFile)}
	opt := dump.Options{
		Log:        logger,
		FileMode:   fm,
		DirMode:    dm,
		SortMemory: sortBytes,
		DBs:        fs.Args(),
		OutDir:     output,
		// Nil when the flag is not given, so the options stay as they were.
		ExcludeTables: excludeTables,
		CaseSafeNames: caseSafeNames,

		ProgressInterval: progressInterval,
		ProgressBar:      bar,
	}
	// dump builds the MySQL writer from Data; another writer takes the options
	// itself, and dump refuses both.
	switch target {
	case targetSQLite:
		opt.Writer = writer.NewSQLite(data)
	case targetPostgres:
		opt.Writer = writer.NewPostgres(data).WithSchemaNames(schemaNames)
	default:
		opt.Data = data
		// Only the MySQL writer can keep a DEFINER: SQLite and PostgreSQL write
		// no routines, triggers or events, and convert views without the clause,
		// so the flag does nothing there (B-111).
		opt.KeepDefiner = definer == definerKeep
		// The same for SQL SECURITY (B-112): no routines are written for these
		// targets and their views lose SQL SECURITY, so nothing is rewritten.
		opt.SQLSecurityInvoker = sqlSecurity == sqlSecurityInvoker
	}
	if maskRules != "" {
		rules, err := mask.Load(maskRules, mask.DefaultRegistry())
		if err != nil {
			return dump.Options{}, fmt.Errorf("--mask-rules: %w", err)
		}
		opt.Mask = rules
		opt.StrictColumns = strictColumns
		// A salt that is given is checked whether or not a transform uses it, so
		// a wrong one is found before it is needed. MYSQLDANCE_SALT alone, without
		// rules, is ignored: it may be set for every run of a machine.
		if saltFile != "" || getenv(mask.SaltEnv) != "" {
			salt, err := mask.LoadSalt(saltFile, getenv)
			if err != nil {
				return dump.Options{}, fmt.Errorf("salt: %w", err)
			}
			opt.MaskSalt = &salt
		}
	}
	opt.Source.Host, opt.Source.Port, opt.Source.Socket, opt.Source.User = host, port, socket, user
	opt.Source.SSLMode, opt.Source.SSLCA = sslMode, sslCA
	opt.Source.NoLock = noLock
	opt.Source.Jobs = jobs
	opt.Source.LockTimeout = lockTimeout
	opt.Source.ConnectTimeout, opt.Source.ReadTimeout = connectTimeout, readTimeout
	if opt.Source.Password, err = loadPassword(passwordFile, getenv, logger); err != nil {
		return dump.Options{}, err
	}
	return opt, nil
}
