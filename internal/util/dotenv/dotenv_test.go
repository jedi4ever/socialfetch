package dotenv

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDoesNotOverride(t *testing.T) {
	t.Setenv("ALREADY_SET", "from-shell")

	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	if err := os.WriteFile(path, []byte(
		"ALREADY_SET=from-file\n"+
			"FRESH=ok\n",
	), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := Load(path); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("ALREADY_SET"); got != "from-shell" {
		t.Errorf("override happened: %q", got)
	}
	if got := os.Getenv("FRESH"); got != "ok" {
		t.Errorf("FRESH not loaded: %q", got)
	}
}

func TestLoadMissingFileIsOK(t *testing.T) {
	if err := Load(filepath.Join(t.TempDir(), "nope")); err != nil {
		t.Errorf("missing file should not error: %v", err)
	}
}

func TestParseLine(t *testing.T) {
	cases := []struct {
		in   string
		k, v string
		ok   bool
	}{
		{`FOO=bar`, "FOO", "bar", true},
		{`FOO=bar  `, "FOO", "bar", true},
		{`FOO=bar # trailing`, "FOO", "bar", true},
		{`export FOO=bar`, "FOO", "bar", true},
		{`FOO="hello world"`, "FOO", "hello world", true},
		{`FOO="line\nbreak"`, "FOO", "line\nbreak", true},
		{`FOO='no \n expand'`, "FOO", `no \n expand`, true},
		{`FOO='has # hash'`, "FOO", "has # hash", true},
		{`# comment`, "", "", false},
		{``, "", "", false},
		{`=novalue`, "", "", false},
		{`NOEQ`, "", "", false},
	}
	for _, c := range cases {
		k, v, ok := parseLine(c.in)
		if k != c.k || v != c.v || ok != c.ok {
			t.Errorf("parseLine(%q) = %q,%q,%v; want %q,%q,%v",
				c.in, k, v, ok, c.k, c.v, c.ok)
		}
	}
}
