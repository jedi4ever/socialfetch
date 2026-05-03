//go:build integration

// End-to-end integration tests that drive the actual social-fetch
// and social-ledger binaries (via go build + exec) instead of
// calling into internal packages. Verifies the full data path:
//
//	social-fetch fetch <url>     →  HTTP GET against an httptest server
//	                            →  rendered output on stdout
//	(when SOCIALFETCH_LEDGER=1)
//	                            →  subprocess to social-ledger
//	                            →  SQLite + mirror tree on disk
//
// Run with:
//
//	go test -tags=integration ./cmd/social-fetch/
//
// Skipped by default so `go test ./...` stays fast and offline-only.
// We build the binaries fresh each test (cached afterward by go's
// build cache) so the assertions cover the real exec'd path, not
// internal Go-call paths that would mask wiring bugs.
package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// buildBinaries compiles social-fetch and social-ledger into a
// shared temp dir, returning their absolute paths. Build cost is
// amortised by go's cache, so successive tests in this package
// reuse the same compiled output.
func buildBinaries(t *testing.T) (sf string, ledger string) {
	t.Helper()
	dir := t.TempDir()
	sf = filepath.Join(dir, "social-fetch")
	ledger = filepath.Join(dir, "social-ledger")

	for _, b := range []struct{ out, pkg string }{
		{sf, "../social-fetch"},
		{ledger, "../social-ledger"},
	} {
		cmd := exec.Command("go", "build", "-o", b.out, b.pkg)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build %s: %v\n%s", b.pkg, err, out)
		}
	}
	return sf, ledger
}

// fakeArticle stands up an httptest server that returns a minimal
// HTML page with OpenGraph metadata. The article fetcher's catch-
// all path will scrape it without auth — keeps the integration
// test from depending on real upstream availability.
func fakeArticle(t *testing.T) *httptest.Server {
	t.Helper()
	const body = `<!doctype html>
<html><head>
<meta property="og:title" content="The Integration Test Article">
<meta property="og:description" content="A short description for the test.">
</head><body>
<article><h1>The Integration Test Article</h1>
<p>This article exists only inside an httptest server. Its purpose is to
prove the fetch + ledger pipeline works end-to-end without burning
real-world API quota.</p></article>
</body></html>`
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(body))
	}))
}

// TestFetchExplicitlyDisabled verifies SOCIALFETCH_LEDGER=0 wins
// over a present binary — the explicit off-switch beats the
// auto-detect default.
func TestFetchExplicitlyDisabled(t *testing.T) {
	sf, ledger := buildBinaries(t)
	srv := fakeArticle(t)
	defer srv.Close()
	dataDir := t.TempDir()

	cmd := exec.Command(sf, "fetch", srv.URL, "--no-comments")
	cmd.Env = append(os.Environ(),
		"SOCIALFETCH_LEDGER=0",
		"SOCIALFETCH_LEDGER_BIN="+ledger,
		"SOCIALFETCH_LEDGER_DIR="+dataDir,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("fetch: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "The Integration Test Article") {
		t.Errorf("expected article title in output, got:\n%s", out)
	}
	entries, _ := os.ReadDir(dataDir)
	if len(entries) != 0 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("ledger dir should be empty when LEDGER=0, got: %v", names)
	}
}

// TestFetchWithLedger verifies SOCIALFETCH_LEDGER=1 routes the
// fetched item into the ledger via subprocess: SQLite db is created,
// `social-ledger list` reports the item, mirror tree contains
// the article markdown.
func TestFetchWithLedger(t *testing.T) {
	sf, ledger := buildBinaries(t)
	srv := fakeArticle(t)
	defer srv.Close()
	dataDir := t.TempDir()

	cmd := exec.Command(sf, "fetch", srv.URL, "--no-comments")
	cmd.Env = append(os.Environ(),
		"SOCIALFETCH_LEDGER=1",
		"SOCIALFETCH_LEDGER_BIN="+ledger,
		"SOCIALFETCH_LEDGER_DIR="+dataDir,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("fetch: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "The Integration Test Article") {
		t.Errorf("expected article title in stdout, got:\n%s", out)
	}

	// SQLite db should exist.
	if _, err := os.Stat(filepath.Join(dataDir, "ledger.db")); err != nil {
		t.Fatalf("ledger.db not created: %v", err)
	}
	// Markdown mirror tree should have the item somewhere under tree/.
	treeDir := filepath.Join(dataDir, "tree")
	if _, err := os.Stat(treeDir); err != nil {
		t.Fatalf("tree dir not created: %v", err)
	}
	var found bool
	filepath.Walk(treeDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if strings.HasSuffix(path, ".md") {
			b, _ := os.ReadFile(path)
			if strings.Contains(string(b), srv.URL) {
				found = true
			}
		}
		return nil
	})
	if !found {
		t.Error("ledger tree contains no markdown file referencing the fetched URL")
	}

	// `social-ledger list` should report exactly 1 item.
	listCmd := exec.Command(ledger, "list", "--data-dir", dataDir)
	listOut, err := listCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("ledger list: %v\n%s", err, listOut)
	}
	if !strings.Contains(string(listOut), srv.URL) {
		t.Errorf("ledger list missing fetched URL, got:\n%s", listOut)
	}
	if !strings.Contains(string(listOut), "list: 1 item(s)") {
		t.Errorf("ledger list count != 1, got:\n%s", listOut)
	}
}

// TestFetchLedgerSurvivesMissingBinary covers the failure mode that
// motivated the "best-effort, swallow errors" design: if the user
// sets SOCIALFETCH_LEDGER=1 but never installed social-ledger,
// the parent fetch still succeeds. The ledger failure shows up in
// the audit log only, never as a non-zero exit on the parent.
func TestFetchLedgerSurvivesMissingBinary(t *testing.T) {
	sf, _ := buildBinaries(t)
	srv := fakeArticle(t)
	defer srv.Close()

	cmd := exec.Command(sf, "fetch", srv.URL, "--no-comments")
	cmd.Env = append(os.Environ(),
		"SOCIALFETCH_LEDGER=1",
		"SOCIALFETCH_LEDGER_BIN=/nonexistent/social-ledger",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("fetch should still succeed when ledger binary is missing: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "The Integration Test Article") {
		t.Errorf("expected article title in stdout despite ledger miss, got:\n%s", out)
	}
}

// TestFetchAutoDetectEnabled confirms the new tri-state default:
// SOCIALFETCH_LEDGER unset + a discoverable binary auto-enables
// the ingest. This is the path most users will hit if they install
// both binaries — no env-var tinkering required.
func TestFetchAutoDetectEnabled(t *testing.T) {
	sf, ledger := buildBinaries(t)
	srv := fakeArticle(t)
	defer srv.Close()
	dataDir := t.TempDir()

	clean := []string{}
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "SOCIALFETCH_LEDGER=") {
			clean = append(clean, kv)
		}
	}
	cmd := exec.Command(sf, "fetch", srv.URL, "--no-comments")
	cmd.Env = append(clean,
		"SOCIALFETCH_LEDGER_BIN="+ledger,
		"SOCIALFETCH_LEDGER_DIR="+dataDir,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("fetch: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "The Integration Test Article") {
		t.Errorf("expected article title in output, got:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "ledger.db")); err != nil {
		t.Fatalf("ledger.db should be created via auto-detect, got: %v", err)
	}
}

// TestFetchAutoDetectDisabled confirms auto-detect stays off when
// no binary is discoverable — env unset, BIN unset, $PATH cleared.
// The fetch still works; the parent never sees a failure.
func TestFetchAutoDetectDisabled(t *testing.T) {
	sf, _ := buildBinaries(t)
	srv := fakeArticle(t)
	defer srv.Close()

	clean := []string{}
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "SOCIALFETCH_LEDGER") && !strings.HasPrefix(kv, "PATH=") {
			clean = append(clean, kv)
		}
	}
	cmd := exec.Command(sf, "fetch", srv.URL, "--no-comments")
	cmd.Env = append(clean, "PATH=/nonexistent")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("fetch: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "The Integration Test Article") {
		t.Errorf("expected article title in output, got:\n%s", out)
	}
}

// TestFetchRedirectStampsRequestURL drives the actual binary
// against a redirect chain and confirms the JSONL output (which
// is what the ledger consumes) carries both URLs: `url` =
// post-redirect target, `request_url` = original input. This is
// the end-to-end glue test for the Registry-stamps-RequestURL
// behavior + article fetcher's redirect capture + JSON omitempty
// shape.
func TestFetchRedirectStampsRequestURL(t *testing.T) {
	sf, _ := buildBinaries(t)

	// final server — destination
	final := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(`<!doctype html><html><head><title>Destination</title></head><body><article><p>real content</p></article></body></html>`))
	}))
	defer final.Close()

	// short server — redirects to final
	short := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, final.URL+"/article", http.StatusMovedPermanently)
	}))
	defer short.Close()

	rawURL := short.URL + "/abc"
	cmd := exec.Command(sf, "fetch", rawURL, "-f", "json", "--no-comments")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("fetch: %v\n%s", err, out)
	}
	body := string(out)
	if !strings.Contains(body, `"url"`) {
		t.Fatalf("output missing url field:\n%s", body)
	}
	wantURL := final.URL + "/article"
	if !strings.Contains(body, `"url": "`+wantURL+`"`) {
		t.Errorf("expected url=%q in output, got:\n%s", wantURL, body)
	}
	if !strings.Contains(body, `"request_url": "`+rawURL+`"`) {
		t.Errorf("expected request_url=%q in output, got:\n%s", rawURL, body)
	}
}
