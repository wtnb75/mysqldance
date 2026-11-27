package source

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/go-sql-driver/mysql"
)

// driverLogger passes what go-sql-driver/mysql would write to standard error to
// a slog.Logger, so that --log-format and --log-level apply to it (B-117). It is
// set on each connection (mysql.Config.Logger), never with mysql.SetLogger, which
// is global to the process.
type driverLogger struct {
	lg       *slog.Logger
	password string
}

// newDriverLogger returns the adapter. The driver is never given a DSN, so its
// text should not hold the password; if it does, the password is replaced
// (password is empty when there is none, and then nothing is replaced).
func newDriverLogger(lg *slog.Logger, password string) mysql.Logger {
	return &driverLogger{lg: lg, password: password}
}

// Print writes one WARN record: a fixed message, and the text of the driver in
// the attribute "message", as the driver wrote it (without its "[mysql] " prefix,
// which the driver's own log.Logger adds and this one does not).
func (d *driverLogger) Print(v ...any) {
	msg := fmt.Sprint(v...)
	// A safety net by the spec (B-117, R-245), not a guarantee: a short password
	// may also garble ordinary text, and a form of the password that the driver
	// writes differently (escaped) is not found. The driver gets no DSN, so the
	// password should not be in its text at all.
	if d.password != "" {
		msg = strings.ReplaceAll(msg, d.password, "***")
	}
	d.lg.Warn("mysql driver", "message", msg)
}
