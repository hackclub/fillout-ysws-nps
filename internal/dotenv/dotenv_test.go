package dotenv

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadSetsAndSkipsExisting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	content := `
# a comment
export EXPORTED_KEY=exported_value
PLAIN=plain_value
QUOTED="quoted value"
SINGLE='single value'
SPACED   =   spaced_value
ALREADY_SET=from_file
EMPTY=
bad line without equals
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("ALREADY_SET", "from_env")

	if err := Load(path); err != nil {
		t.Fatalf("Load: %v", err)
	}

	cases := map[string]string{
		"EXPORTED_KEY": "exported_value",
		"PLAIN":        "plain_value",
		"QUOTED":       "quoted value",
		"SINGLE":       "single value",
		"SPACED":       "spaced_value",
		"ALREADY_SET":  "from_env", // not overridden
		"EMPTY":        "",
	}
	for key, want := range cases {
		if got := os.Getenv(key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
}

func TestLoadMissingFileIsNoError(t *testing.T) {
	if err := Load(filepath.Join(t.TempDir(), "does-not-exist.env")); err != nil {
		t.Fatalf("expected nil error for missing file, got %v", err)
	}
}

func TestParseLine(t *testing.T) {
	cases := []struct {
		in        string
		wantKey   string
		wantValue string
		wantOK    bool
	}{
		{"FOO=bar", "FOO", "bar", true},
		{"  FOO = bar ", "FOO", "bar", true},
		{`FOO="bar baz"`, "FOO", "bar baz", true},
		{"export FOO=bar", "FOO", "bar", true},
		{"# comment", "", "", false},
		{"", "", "", false},
		{"noequals", "", "", false},
		{"=novalue", "", "", false},
	}
	for _, tc := range cases {
		key, value, ok := parseLine(tc.in)
		if ok != tc.wantOK || key != tc.wantKey || value != tc.wantValue {
			t.Errorf("parseLine(%q) = (%q,%q,%v), want (%q,%q,%v)",
				tc.in, key, value, ok, tc.wantKey, tc.wantValue, tc.wantOK)
		}
	}
}
