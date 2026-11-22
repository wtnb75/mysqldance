package main

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

// termBuffer is a standard error that says it is a terminal of cols columns.
type termBuffer struct {
	bytes.Buffer
	tty  bool
	cols int
}

func (b *termBuffer) terminal() (bool, int) { return b.tty, b.cols }

// --progress picks the bar or the records (B-203): auto follows the terminal, bar
// forces the bar, log and none never draw one, and only log keeps the interval.
func TestParseArgsProgressMode(t *testing.T) {
	for _, tt := range []struct {
		name         string
		args         []string
		tty          bool
		wantBar      bool
		wantInterval time.Duration
	}{
		{"auto on a terminal", nil, true, true, 0},
		{"auto without a terminal", nil, false, false, 10 * time.Second},
		{"explicit auto on a terminal", []string{"--progress", "auto"}, true, true, 0},
		{"bar without a terminal", []string{"--progress", "bar"}, false, true, 0},
		{"log on a terminal", []string{"--progress", "log"}, true, false, 10 * time.Second},
		{"log with an interval", []string{"--progress", "log", "--progress-interval", "30s"}, true, false, 30 * time.Second},
		{"none on a terminal", []string{"--progress", "none"}, true, false, 0},
		{"none without a terminal", []string{"--progress", "none"}, false, false, 0},
		{"bar ignores the interval", []string{"--progress", "bar", "--progress-interval", "30s"}, false, true, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			stderr := &termBuffer{tty: tt.tty, cols: 100}
			got, err := parseArgsTo(append(tt.args, "-o", "out", "d"), noEnv, stderr)
			if err != nil {
				t.Fatal(err)
			}
			if (got.ProgressBar != nil) != tt.wantBar {
				t.Errorf("ProgressBar set = %v, want %v", got.ProgressBar != nil, tt.wantBar)
			}
			if got.ProgressInterval != tt.wantInterval {
				t.Errorf("ProgressInterval = %v, want %v", got.ProgressInterval, tt.wantInterval)
			}
		})
	}
}

// With the bar, the logger writes through it, so a record reaches standard error
// whole; without the bar it writes to standard error as it did.
func TestParseArgsBarCarriesTheLog(t *testing.T) {
	for _, mode := range []string{"bar", "log"} {
		stderr := &termBuffer{cols: 100}
		got, err := parseArgsTo([]string{"--progress", mode, "-o", "out", "d"}, noEnv, stderr)
		if err != nil {
			t.Fatal(err)
		}
		got.Log.Info("hello")
		if !strings.HasSuffix(stderr.String(), "msg=hello\n") {
			t.Errorf("--progress %s: stderr = %q, want the record", mode, stderr.String())
		}
	}
}

func TestTerminalColumns(t *testing.T) {
	for _, tt := range []struct {
		name    string
		term    int
		columns string
		want    int
	}{
		{"terminal width", 120, "50", 120},
		{"COLUMNS", 0, "50", 50},
		{"COLUMNS not a number", 0, "wide", 80},
		{"COLUMNS not positive", 0, "0", 80},
		{"nothing", 0, "", 80},
	} {
		getenv := func(k string) string {
			if k == "COLUMNS" {
				return tt.columns
			}
			return ""
		}
		if got := columnsOf(tt.term, getenv); got != tt.want {
			t.Errorf("%s: columnsOf = %d, want %d", tt.name, got, tt.want)
		}
	}
}

// A writer that is not a file (a buffer) is no terminal.
func TestTerminalOfBuffer(t *testing.T) {
	if tty, cols := terminalOf(&bytes.Buffer{}); tty || cols != 0 {
		t.Errorf("terminalOf(buffer) = %v, %d, want false, 0", tty, cols)
	}
}
