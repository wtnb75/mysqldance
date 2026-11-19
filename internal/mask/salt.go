package mask

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"runtime"
)

// SaltEnv is the environment variable the salt is read from when no salt file
// is given.
const SaltEnv = "MYSQLDANCE_SALT"

// MinSaltLen is the shortest salt accepted, in bytes.
const MinSaltLen = 16

// Salt is the secret mixed into every masked value. It never prints: the
// formatting verbs and slog show a placeholder, so it cannot leak into a log or
// an error by accident. Bytes returns the value.
type Salt struct {
	b []byte
}

// Bytes returns the salt.
func (s Salt) Bytes() []byte { return s.b }

// String returns a placeholder, never the value.
func (Salt) String() string { return "[salt]" }

// GoString returns a placeholder, never the value.
func (Salt) GoString() string { return "mask.Salt{[salt]}" }

// LogValue keeps the value out of structured logs.
func (Salt) LogValue() slog.Value { return slog.StringValue("[salt]") }

// LoadSalt reads the salt: from the file when saltFile is not empty, else from
// the environment variable MYSQLDANCE_SALT. There is no default, and the value
// is not taken from a flag (it would show in the process list). A file wins
// over the environment; one trailing newline is cut off a file, as with
// --password-file. An empty salt, one shorter than MinSaltLen, and a file that
// group or others can read are errors; no message carries the value.
func LoadSalt(saltFile string, getenv func(string) string) (Salt, error) {
	var b []byte
	switch {
	case saltFile != "":
		data, err := readSecretFile(saltFile)
		if err != nil {
			return Salt{}, err
		}
		b = trimNewline(data)
	case getenv(SaltEnv) != "":
		b = []byte(getenv(SaltEnv))
	default:
		return Salt{}, fmt.Errorf("no salt: set %s or pass --salt-file (there is no default salt)", SaltEnv)
	}
	if len(b) == 0 {
		return Salt{}, errors.New("the salt is empty")
	}
	if len(b) < MinSaltLen {
		return Salt{}, fmt.Errorf("the salt is %d bytes; it must be at least %d bytes", len(b), MinSaltLen)
	}
	return Salt{b: b}, nil
}

// readSecretFile reads the file, refusing one that group or others can read
// (an error, not a warning as for --password-file: a leaked salt lets a
// dictionary bring low-entropy values back).
// The mode is taken from the opened file, so it is the mode of the bytes read.
// Windows mode bits carry no access rules, so nothing is checked there.
func readSecretFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("salt file: %w", err)
	}
	defer func() { _ = f.Close() }()
	if runtime.GOOS != "windows" {
		fi, err := f.Stat()
		if err != nil {
			return nil, fmt.Errorf("salt file %q: %w", path, err)
		}
		if perm := fi.Mode().Perm(); perm&0o066 != 0 {
			// Writable counts too: whoever can replace the salt knows it.
			return nil, fmt.Errorf("salt file %q is readable or writable by group or others (mode %04o); restrict it, for example with chmod 600", path, perm)
		}
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, fmt.Errorf("salt file %q: %w", path, err)
	}
	return data, nil
}

// trimNewline cuts one trailing "\n" or "\r\n".
func trimNewline(b []byte) []byte {
	trimmed, ok := bytes.CutSuffix(b, []byte("\n"))
	if !ok {
		return b
	}
	return bytes.TrimSuffix(trimmed, []byte("\r"))
}
