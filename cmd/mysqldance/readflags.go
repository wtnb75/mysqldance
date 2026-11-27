package main

import (
	"flag"
	"fmt"
	"io"
	"log/slog"

	"mysqldance/internal/source"
)

// readFlags are the flags that the subcommands which only read (scan-k, scan)
// share: the connection, the tables left out, the lock and the log.
type readFlags struct {
	host, user, socket, passwordFile string
	sslMode, sslCA                   string
	logLevel, logFormat              string
	noLock                           bool
	port                             int
	excludeTables                    stringList
}

// register adds the flags to fs.
func (f *readFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&f.host, "h", "127.0.0.1", "")
	fs.StringVar(&f.host, "host", "127.0.0.1", "")
	fs.IntVar(&f.port, "P", 3306, "")
	fs.IntVar(&f.port, "port", 3306, "")
	fs.StringVar(&f.user, "u", "", "")
	fs.StringVar(&f.user, "user", "", "")
	fs.StringVar(&f.socket, "socket", "", "")
	fs.StringVar(&f.passwordFile, "password-file", "", "")
	fs.StringVar(&f.sslMode, "ssl-mode", source.SSLPreferred, "")
	fs.StringVar(&f.sslCA, "ssl-ca", "", "")
	fs.Var(&f.excludeTables, "exclude-table", "")
	fs.BoolVar(&f.noLock, "no-lock", false, "")
	fs.StringVar(&f.logLevel, "log-level", "info", "")
	fs.StringVar(&f.logFormat, "log-format", "text", "")
}

// resolve checks the flags and returns the connection settings and the logger.
// A wrong flag is reported through usage, which returns the usage error.
func (f *readFlags) resolve(getenv func(string) string, stderr io.Writer, usage func(format string, a ...any) error) (source.Config, *slog.Logger, error) {
	if f.port <= 0 || f.port > 65535 {
		return source.Config{}, nil, usage("--port must be between 1 and 65535")
	}
	cfg := source.Config{
		Host: f.host, Port: f.port, Socket: f.socket, User: f.user,
		SSLMode: f.sslMode, SSLCA: f.sslCA, NoLock: f.noLock, Jobs: 1,
		LockTimeout: source.DefaultLockTimeout, ConnectTimeout: source.DefaultConnectTimeout,
	}
	if err := cfg.ValidateSSL(); err != nil {
		return source.Config{}, nil, usage("%v", err)
	}
	logger, err := newLogger(stderr, f.logLevel, f.logFormat)
	if err != nil {
		return source.Config{}, nil, usage("%v", err)
	}
	for _, spec := range f.excludeTables {
		if _, _, err := source.SplitTableSpec(spec); err != nil {
			return source.Config{}, nil, usage("--exclude-table: %v", err)
		}
	}
	if cfg.Password, err = loadPassword(f.passwordFile, getenv, logger); err != nil {
		return source.Config{}, nil, err
	}
	return cfg, logger, nil
}

// usageFunc returns the usage reporter of a subcommand: it prints the message
// and the usage text, and returns errUsage.
func usageFunc(name string, fs *flag.FlagSet, stderr io.Writer) func(format string, a ...any) error {
	return func(format string, a ...any) error {
		_, _ = fmt.Fprintf(stderr, "mysqldance "+name+": "+format+"\n", a...)
		fs.Usage()
		return errUsage
	}
}
