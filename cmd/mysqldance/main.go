// Command mysqldance dumps MySQL databases into deterministic SQL files.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"mysqldance/internal/datafile"
	"mysqldance/internal/dump"
	"mysqldance/internal/source"
)

func main() {
	os.Exit(run(os.Args[1:], os.Getenv, os.Stderr))
}

// errUsage marks errors already reported by the flag package.
var errUsage = errors.New("usage error")

// run executes the command and returns the process exit code.
func run(args []string, getenv func(string) string, stderr io.Writer) int {
	opt, err := parseArgsTo(args, getenv, stderr)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		if !errors.Is(err, errUsage) {
			_, _ = fmt.Fprintf(stderr, "mysqldance: %v\n", err)
		}
		return 1
	}
	opt.Log = stderr

	ctx, stop := notifyContext(context.Background())
	defer stop()
	if err := dump.Run(ctx, opt); err != nil {
		_, _ = fmt.Fprintf(stderr, "mysqldance: %v\n", err)
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
      --rows-per-insert N    average rows per INSERT statement (default 1000)
      --max-insert-bytes N   size cap of one INSERT statement (default 1048576)
      --no-lock              do not take FLUSH TABLES WITH READ LOCK when a
                             table is not InnoDB (warn instead)
`)
	}

	var (
		host, user, socket, passwordFile, output string
		sslMode, sslCA                           string
		noLock                                   bool
		port                                     int
		rowsPerInsert                            uint64
		maxInsertBytes                           int
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
	fs.Uint64Var(&rowsPerInsert, "rows-per-insert", 1000, "")
	fs.IntVar(&maxInsertBytes, "max-insert-bytes", 1048576, "")
	fs.BoolVar(&noLock, "no-lock", false, "")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return dump.Options{}, err
		}
		return dump.Options{}, errUsage
	}
	usage := func(format string, a ...any) (dump.Options, error) {
		_, _ = fmt.Fprintf(stderr, "mysqldance: "+format+"\n", a...)
		fs.Usage()
		return dump.Options{}, errUsage
	}
	switch {
	case output == "":
		return usage("-o/--output is required")
	case fs.NArg() == 0:
		return usage("at least one database name is required")
	case rowsPerInsert == 0 || rowsPerInsert > math.MaxUint32:
		return usage("--rows-per-insert must be between 1 and %d", uint64(math.MaxUint32))
	case maxInsertBytes <= 0:
		return usage("--max-insert-bytes must be positive")
	case port <= 0 || port > 65535:
		return usage("--port must be between 1 and 65535")
	}
	if err := (source.Config{Socket: socket, SSLMode: sslMode, SSLCA: sslCA}).ValidateSSL(); err != nil {
		return usage("%v", err)
	}

	opt := dump.Options{
		DBs:    fs.Args(),
		OutDir: output,
		Data:   datafile.Options{RowsPerInsert: uint32(rowsPerInsert), MaxInsertBytes: maxInsertBytes},
	}
	opt.Source.Host, opt.Source.Port, opt.Source.Socket, opt.Source.User = host, port, socket, user
	opt.Source.SSLMode, opt.Source.SSLCA = sslMode, sslCA
	opt.Source.NoLock = noLock
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
