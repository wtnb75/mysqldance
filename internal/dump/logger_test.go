package dump

import (
	"io"
	"log/slog"
)

// testLogger returns a text logger writing to w without the time attribute, so
// that logs of different runs can be compared line for line.
func testLogger(w io.Writer) *slog.Logger {
	return testLoggerAt(w, slog.LevelInfo)
}

// testLoggerAt is testLogger with the lowest level that is written.
func testLoggerAt(w io.Writer, level slog.Level) *slog.Logger {
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{
		Level: level,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if len(groups) == 0 && a.Key == slog.TimeKey {
				return slog.Attr{}
			}
			return a
		},
	}))
}
