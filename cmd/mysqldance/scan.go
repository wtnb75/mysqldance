package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"

	"mysqldance/internal/scan"
	"mysqldance/internal/scanrun"
)

// subcommandScan is the name that makes the command scan the values of the
// columns for sensitive data (B-177) instead of dumping. Like scan-k, it counts
// only when it is the first argument and is exactly that, so a database of this
// name is dumped with the flags first: mysqldance -o out scan.
const subcommandScan = "scan"

// The exit codes of scan. A scan that found something still exits 0: failing on
// a finding is the job of --check (B-179).
const (
	scanOK        = 0
	scanCannotRun = 2
)

// defaultScanSets are the built-in rule sets that a scan given no --scan-config
// uses. pii-columns (which matches on the names of columns) is left out; a config
// asks for it with [extend] useBuiltin.
var defaultScanSets = []string{"pii"}

// scanOptions is what the command line of scan holds.
type scanOptions struct {
	scanrun.Options
	format string
}

// runScan runs the scan subcommand and returns the process exit code.
func runScan(args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	opt, err := parseScan(args, getenv, stderr)
	if errors.Is(err, flag.ErrHelp) {
		return scanOK
	}
	if err != nil {
		if !errors.Is(err, errUsage) {
			_, _ = fmt.Fprintf(stderr, "mysqldance scan: %v\n", err)
		}
		return scanCannotRun
	}
	ctx, stop := notifyContext(context.Background())
	defer stop()
	findings, err := scanrun.Execute(ctx, opt.Options)
	if err != nil {
		opt.Log.Error("scan failed", "error", err)
		return scanCannotRun
	}
	switch opt.format {
	case scanKFormatJSON:
		err = scanrun.WriteJSON(stdout, findings)
	default:
		err = scanrun.WriteTable(stdout, findings)
	}
	if err != nil {
		opt.Log.Error("scan failed", "error", err)
		return scanCannotRun
	}
	return scanOK
}

// parseScan parses the command line of scan without connecting.
func parseScan(args []string, getenv func(string) string, stderr io.Writer) (scanOptions, error) {
	fs := flag.NewFlagSet("mysqldance scan", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprint(stderr, `usage: mysqldance scan [flags] DB [DB...]

Scans the values of the columns for sensitive data with gitleaks-compatible
rules, and reports where it found some: the database, table, column and rule,
how many rows matched, and how many rows were scanned. Prints counts only,
never a value. Flags must come before the database names.
Binary columns (BINARY, VARBINARY, BLOB, BIT, spatial) are not scanned, and a
NULL is not scanned. A finding is a hint, not a guarantee that nothing else is
sensitive.
Exit status: 0 scanned (also when something was found), 2 not run.
The password is read from --password-file, else from MYSQL_PWD.

  -h, --host HOST            server host (default 127.0.0.1)
  -P, --port PORT            server port (default 3306)
  -u, --user USER            user name
      --socket PATH          Unix socket (overrides host and port)
      --password-file FILE   file holding the password
      --ssl-mode MODE        disabled, preferred, required, verify-ca or
                             verify-identity (default preferred)
      --ssl-ca FILE          PEM file of CA certificates to verify the server
      --scan-config FILE     a gitleaks-compatible TOML rule file. Without it,
                             only the built-in "pii" rules are used. With it,
                             the file decides (add [extend] useBuiltin to use
                             the built-in sets "pii" and "pii-columns")
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
	var rf readFlags
	var scanConfig, format string
	rf.register(fs)
	fs.StringVar(&scanConfig, "scan-config", "", "")
	fs.StringVar(&format, "format", scanKFormatText, "")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return scanOptions{}, err
		}
		return scanOptions{}, errUsage
	}
	usage := usageFunc("scan", fs, stderr)
	switch {
	case fs.NArg() == 0:
		return scanOptions{}, usage("at least one database name is required")
	case format != scanKFormatText && format != scanKFormatJSON:
		return scanOptions{}, usage("--format must be %s or %s, not %q", scanKFormatText, scanKFormatJSON, format)
	}
	src, logger, err := rf.resolve(getenv, stderr, usage)
	if err != nil {
		return scanOptions{}, err
	}
	var cfg *scan.Config
	if scanConfig == "" {
		cfg, err = scan.WithBuiltin(defaultScanSets...)
	} else {
		cfg, err = scan.Load(scanConfig)
	}
	if err != nil {
		return scanOptions{}, fmt.Errorf("--scan-config: %w", err)
	}
	return scanOptions{
		Options: scanrun.Options{Source: src, DBs: fs.Args(), ExcludeTables: rf.excludeTables, Config: cfg, Log: logger},
		format:  format,
	}, nil
}
