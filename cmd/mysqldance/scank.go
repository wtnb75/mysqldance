package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"mysqldance/internal/kanon"
	"mysqldance/internal/mask"
	"mysqldance/internal/source"
)

// subcommandScanK is the name that makes the command measure k-anonymity
// (B-196) instead of dumping. It counts only when it is the first argument and
// is exactly that, so a database of this name is dumped with the flags first:
// mysqldance -o out scan-k.
const subcommandScanK = "scan-k"

// The exit codes of scan-k. They differ from the dump's 0 and 1: a failed run
// and a measurement that found rows under the threshold must be told apart.
const (
	scanKOK         = 0
	scanKBelow      = 1
	scanKCannotRun  = 2
	scanKFormatText = "table"
	scanKFormatJSON = "json"
)

// scanKOptions is what the command line of scan-k holds.
type scanKOptions struct {
	kanon.Options
	format string
}

// loadPassword returns the password: the first line of the file when given, else
// MYSQL_PWD. A trailing newline is not part of it.
func loadPassword(passwordFile string, getenv func(string) string, logger *slog.Logger) (string, error) {
	pw := getenv("MYSQL_PWD")
	if passwordFile != "" {
		b, err := readPasswordFile(passwordFile, logger)
		if err != nil {
			return "", fmt.Errorf("read password file: %w", err)
		}
		pw = strings.TrimSuffix(strings.TrimSuffix(string(b), "\n"), "\r")
	}
	return pw, nil
}

// runScanK runs the scan-k subcommand and returns the process exit code.
func runScanK(args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	opt, err := parseScanK(args, getenv, stderr)
	if errors.Is(err, flag.ErrHelp) {
		return scanKOK
	}
	if err != nil {
		if !errors.Is(err, errUsage) {
			_, _ = fmt.Fprintf(stderr, "mysqldance scan-k: %v\n", err)
		}
		return scanKCannotRun
	}
	ctx, stop := notifyContext(context.Background())
	defer stop()
	reports, err := kanon.Run(ctx, opt.Options)
	if err != nil {
		opt.Log.Error("scan-k failed", "error", err)
		return scanKCannotRun
	}
	switch opt.format {
	case scanKFormatJSON:
		err = kanon.WriteJSON(stdout, reports)
	default:
		err = kanon.WriteTable(stdout, reports, opt.K > 0)
	}
	if err != nil {
		opt.Log.Error("scan-k failed", "error", err)
		return scanKCannotRun
	}
	if opt.K > 0 && kanon.AnyBelow(reports) {
		opt.Log.Warn("rows with k under the threshold", "k", opt.K)
		return scanKBelow
	}
	return scanKOK
}

// parseScanK parses the command line of scan-k without connecting.
func parseScanK(args []string, getenv func(string) string, stderr io.Writer) (scanKOptions, error) {
	fs := flag.NewFlagSet("mysqldance scan-k", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprint(stderr, `usage: mysqldance scan-k [flags] --mask-rules FILE DB [DB...]

Measures k-anonymity: for each table that has a column marked "quasi: true" in
the rule file, how many rows share each combination of values of those columns.
Prints counts only, never a value. Flags must come before the database names.
Exit status: 0 measured (and no row under --k), 1 rows under --k, 2 not run.
The password is read from --password-file, else from MYSQL_PWD.

  -h, --host HOST            server host (default 127.0.0.1)
  -P, --port PORT            server port (default 3306)
  -u, --user USER            user name
      --socket PATH          Unix socket (overrides host and port)
      --password-file FILE   file holding the password
      --ssl-mode MODE        disabled, preferred, required, verify-ca or
                             verify-identity (default preferred)
      --ssl-ca FILE          PEM file of CA certificates to verify the server
      --mask-rules FILE      the rule file (required); the columns with
                             "quasi: true" make up the quasi-identifier of
                             their table
      --k N                  exit with status 1 if a row is in a group of fewer
                             than N rows (N is at least 1; default: only report)
      --format FORMAT        table or json (one JSON object per line); default
                             table
      --exclude-table DB.TABLE
                             leave a table out, as in a dump. Repeatable
      --no-lock              do not take FLUSH TABLES WITH READ LOCK when a
                             table is not InnoDB (warn instead)
      --log-level LEVEL      debug, info, warn or error (default info). Logs go
                             to standard error
      --log-format FORMAT    text or json (default text)
`)
	}
	var excludeTables stringList
	var (
		host, user, socket, passwordFile string
		sslMode, sslCA                   string
		maskRules, format                string
		logLevel, logFormat              string
		noLock                           bool
		port                             int
		k                                int64
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
	fs.StringVar(&maskRules, "mask-rules", "", "")
	fs.Int64Var(&k, "k", 0, "")
	fs.StringVar(&format, "format", scanKFormatText, "")
	fs.Var(&excludeTables, "exclude-table", "")
	fs.BoolVar(&noLock, "no-lock", false, "")
	fs.StringVar(&logLevel, "log-level", "info", "")
	fs.StringVar(&logFormat, "log-format", "text", "")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return scanKOptions{}, err
		}
		return scanKOptions{}, errUsage
	}
	kGiven := false
	fs.Visit(func(f *flag.Flag) { kGiven = kGiven || f.Name == "k" })
	usage := func(format string, a ...any) (scanKOptions, error) {
		_, _ = fmt.Fprintf(stderr, "mysqldance scan-k: "+format+"\n", a...)
		fs.Usage()
		return scanKOptions{}, errUsage
	}
	switch {
	case maskRules == "":
		return usage("--mask-rules is required: the columns to measure are the ones marked quasi in it")
	case fs.NArg() == 0:
		return usage("at least one database name is required")
	case kGiven && k < 1:
		return usage("--k must be at least 1")
	case format != scanKFormatText && format != scanKFormatJSON:
		return usage("--format must be %s or %s, not %q", scanKFormatText, scanKFormatJSON, format)
	case port <= 0 || port > 65535:
		return usage("--port must be between 1 and 65535")
	}
	if err := (source.Config{Socket: socket, SSLMode: sslMode, SSLCA: sslCA}).ValidateSSL(); err != nil {
		return usage("%v", err)
	}
	logger, err := newLogger(stderr, logLevel, logFormat)
	if err != nil {
		return usage("%v", err)
	}
	for _, spec := range excludeTables {
		if _, _, err := source.SplitTableSpec(spec); err != nil {
			return usage("--exclude-table: %v", err)
		}
	}
	rules, err := mask.Load(maskRules, mask.DefaultRegistry())
	if err != nil {
		return scanKOptions{}, fmt.Errorf("--mask-rules: %w", err)
	}
	opt := scanKOptions{format: format}
	opt.DBs, opt.ExcludeTables, opt.Rules, opt.K, opt.Log = fs.Args(), excludeTables, rules, k, logger
	opt.Source.Host, opt.Source.Port, opt.Source.Socket, opt.Source.User = host, port, socket, user
	opt.Source.SSLMode, opt.Source.SSLCA = sslMode, sslCA
	opt.Source.NoLock = noLock
	opt.Source.Jobs = 1
	opt.Source.LockTimeout = source.DefaultLockTimeout
	opt.Source.ConnectTimeout = source.DefaultConnectTimeout
	if opt.Source.Password, err = loadPassword(passwordFile, getenv, logger); err != nil {
		return scanKOptions{}, err
	}
	return opt, nil
}
