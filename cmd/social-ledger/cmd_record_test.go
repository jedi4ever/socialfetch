package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jedi4ever/social-skills/internal/ledger/store"
)

// TestCmdRecordBasic verifies the happy path: record a URL with
// content via --content FILE (avoiding stdin contortions in
// unit-test land), then assert the row landed in the store with
// the right shape.
func TestCmdRecordBasic(t *testing.T) {
	dir := t.TempDir()
	contentFile := filepath.Join(t.TempDir(), "body.md")
	if err := os.WriteFile(contentFile, []byte("# Test Article\n\nbody"), 0o644); err != nil {
		t.Fatal(err)
	}

	args := []string{
		"--data-dir", dir,
		"--title", "Test Article",
		"--source", "webfetch",
		"--summary", "a short summary",
		"--content", contentFile,
		"https://example.com/test",
	}
	if err := cmdRecord(args); err != nil {
		t.Fatalf("cmdRecord: %v", err)
	}

	// Open the same store and verify the row exists with the
	// expected fields.
	s, err := store.Open(filepath.Join(dir, "ledger.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	got, err := s.Get("webfetch::https://example.com/test")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got == nil {
		t.Fatal("get returned nil — row not found")
	}
	if got.Title != "Test Article" {
		t.Errorf("title=%q, want %q", got.Title, "Test Article")
	}
	if got.Source != "webfetch" {
		t.Errorf("source=%q, want webfetch", got.Source)
	}
	if !strings.Contains(got.Content, "body") {
		t.Errorf("content missing body, got: %q", got.Content)
	}
}

// TestCmdRecordMissingTitle — empty / missing --title is a hard
// error. Empty titles yield useless ledger entries (nothing
// usable in `list` output, no FTS5 hits via the title column).
func TestCmdRecordMissingTitle(t *testing.T) {
	dir := t.TempDir()
	args := []string{"--data-dir", dir, "https://example.com/test"}
	if err := cmdRecord(args); err == nil {
		t.Fatal("expected error for missing --title, got nil")
	}
}

// TestCmdRecordIdempotent — recording the same URL twice with
// identical content yields the IngestUnchanged path; stats reports
// 1 row, not 2. Lets agents call record liberally without
// worrying about duplicate-key explosions.
func TestCmdRecordIdempotent(t *testing.T) {
	dir := t.TempDir()
	contentFile := filepath.Join(t.TempDir(), "body.md")
	os.WriteFile(contentFile, []byte("identical"), 0o644)
	args := []string{
		"--data-dir", dir,
		"--title", "X",
		"--content", contentFile,
		"https://example.com/x",
	}
	for i := 0; i < 3; i++ {
		if err := cmdRecord(args); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	s, err := store.Open(filepath.Join(dir, "ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	st, err := s.Stats()
	if err != nil {
		t.Fatal(err)
	}
	if st.Total != 1 {
		t.Errorf("after 3 identical records, Total=%d, want 1", st.Total)
	}
}
