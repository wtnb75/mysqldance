package source

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

func jsonLogger(buf *bytes.Buffer, level slog.Level) *slog.Logger {
	return slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: level}))
}

func TestDriverLoggerWritesAWarnRecord(t *testing.T) {
	var buf bytes.Buffer
	l := newDriverLogger(jsonLogger(&buf, slog.LevelDebug), "")
	l.Print(errors.New("read tcp 1.2.3.4:5->6.7.8.9:3306: i/o timeout"))

	var rec map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
		t.Fatalf("not one JSON record: %v\n%s", err, buf.String())
	}
	if rec["level"] != "WARN" || rec["msg"] != "mysql driver" {
		t.Errorf("record = %v", rec)
	}
	if got, _ := rec["message"].(string); !strings.Contains(got, "i/o timeout") {
		t.Errorf("message = %q", got)
	}
}

func TestDriverLoggerJoinsItsArguments(t *testing.T) {
	var buf bytes.Buffer
	l := newDriverLogger(jsonLogger(&buf, slog.LevelDebug), "")
	l.Print("could not use requested auth plugin 'x': ", "unknown")
	var rec map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
		t.Fatal(err)
	}
	if rec["message"] != "could not use requested auth plugin 'x': unknown" {
		t.Errorf("message = %v", rec["message"])
	}
}

func TestDriverLoggerIsSilentAtLevelError(t *testing.T) {
	var buf bytes.Buffer
	newDriverLogger(jsonLogger(&buf, slog.LevelError), "").Print("i/o timeout")
	if buf.Len() != 0 {
		t.Errorf("logged at level error: %s", buf.String())
	}
}

func TestDriverLoggerHidesThePassword(t *testing.T) {
	var buf bytes.Buffer
	l := newDriverLogger(jsonLogger(&buf, slog.LevelDebug), "s3cret")
	l.Print("access denied with s3cret and again s3cret")
	if strings.Contains(buf.String(), "s3cret") {
		t.Errorf("the record holds the password: %s", buf.String())
	}
	if !strings.Contains(buf.String(), "access denied with *** and again ***") {
		t.Errorf("record = %s", buf.String())
	}
}

func TestDriverLoggerKeepsTheTextWhenThereIsNoPassword(t *testing.T) {
	var buf bytes.Buffer
	newDriverLogger(jsonLogger(&buf, slog.LevelDebug), "").Print("a  b")
	if !strings.Contains(buf.String(), `"message":"a  b"`) {
		t.Errorf("record = %s", buf.String())
	}
}

// Open must give the driver this logger (and not leave the one that writes to
// standard error), with the host and port of the connection on every record.
func TestDriverConfigUsesTheSlogAdapter(t *testing.T) {
	var buf bytes.Buffer
	mc, lg, err := driverConfig(Config{Host: "db.example", Port: 3307, Password: "s3cret", Log: jsonLogger(&buf, slog.LevelDebug), SSLMode: SSLDisabled})
	if err != nil {
		t.Fatal(err)
	}
	if lg == nil || mc == nil || mc.Logger == nil {
		t.Fatalf("mc = %v, lg = %v: the logger is not set", mc, lg)
	}
	if mc.Passwd != "s3cret" {
		t.Errorf("Passwd = %q", mc.Passwd)
	}
	mc.Logger.Print("i/o timeout, password s3cret")
	var rec map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
		t.Fatalf("not a JSON record: %v\n%s", err, buf.String())
	}
	if rec["level"] != "WARN" || rec["host"] != "db.example" || rec["port"] != float64(3307) {
		t.Errorf("record = %v", rec)
	}
	if strings.Contains(buf.String(), "s3cret") {
		t.Errorf("the record holds the password: %s", buf.String())
	}
}

func TestDriverConfigWithoutALoggerDiscards(t *testing.T) {
	mc, _, err := driverConfig(Config{SSLMode: SSLDisabled})
	if err != nil {
		t.Fatal(err)
	}
	if mc.Logger == nil {
		t.Fatal("mc.Logger is not set")
	}
	mc.Logger.Print("i/o timeout") // must not panic or write anywhere
}
