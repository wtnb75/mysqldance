package main

import (
	"bytes"
	"io"
	"testing"
)

func TestParseSize(t *testing.T) {
	for _, tt := range []struct {
		in   string
		want int64
	}{
		{"1048576", 1 << 20},
		{"1MiB", 1 << 20},
		{"64MiB", 64 << 20},
		{"256mib", 256 << 20},
		{"2GiB", 2 << 30},
		{"512KiB", 512 << 10},
		{"1MB", 1 << 20},
		{"1G", 1 << 30},
	} {
		got, err := parseSize(tt.in)
		if err != nil || got != tt.want {
			t.Errorf("parseSize(%q) = %d, %v; want %d", tt.in, got, err, tt.want)
		}
	}
	for _, in := range []string{"", "abc", "-1MiB", "1.5MiB", "MiB", "10XiB", "99999999999999GiB"} {
		if got, err := parseSize(in); err == nil {
			t.Errorf("parseSize(%q) = %d, want an error", in, got)
		}
	}
}

func TestSortMemoryFlag(t *testing.T) {
	got, err := parseArgs([]string{"--sort-memory", "256MiB", "-o", "out", "d"}, noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if got.SortMemory != 256<<20 {
		t.Errorf("SortMemory = %d, want %d", got.SortMemory, 256<<20)
	}
	got, err = parseArgs([]string{"-o", "out", "d"}, noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if got.SortMemory != 0 {
		t.Errorf("SortMemory = %d without the flag, want 0 (the default of dump)", got.SortMemory)
	}
}

func TestSortMemoryFlagRejects(t *testing.T) {
	for _, v := range []string{"1023KiB", "0", "-5", "lots", "1.5GiB"} {
		var stderr bytes.Buffer
		args := []string{"--sort-memory", v, "-o", t.TempDir(), "db1"}
		if code := run(args, noEnv, io.Discard, &stderr); code != 1 || stderr.Len() == 0 {
			t.Errorf("run(%q) = %d, stderr %q; want a usage error", args, code, stderr.String())
		}
	}
}
