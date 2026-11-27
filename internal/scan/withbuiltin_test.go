package scan

import (
	"strings"
	"testing"
)

func TestWithBuiltinIsTheSameAsUseBuiltin(t *testing.T) {
	got, err := WithBuiltin("pii")
	if err != nil {
		t.Fatal(err)
	}
	want := mustParse(t, "[extend]\nuseBuiltin = [\"pii\"]\n")
	if len(got.rules) == 0 || len(got.rules) != len(want.rules) {
		t.Fatalf("rules = %d, want %d", len(got.rules), len(want.rules))
	}
	m := got.Scan("shop.users.email", "alice@example.com")
	if len(m) != 1 || m[0].RuleID != "pii-email" {
		t.Errorf("matches = %+v", m)
	}
}

func TestWithBuiltinNamesNothingIsAnError(t *testing.T) {
	// A config with no rule would report "nothing found" for every value.
	if _, err := WithBuiltin(); err == nil {
		t.Error("no name accepted")
	}
}

func TestWithBuiltinUnknownName(t *testing.T) {
	_, err := WithBuiltin("nope")
	if err == nil || !strings.Contains(err.Error(), `"nope"`) {
		t.Errorf("err = %v", err)
	}
}
