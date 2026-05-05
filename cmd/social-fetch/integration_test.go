//go:build integration

// End-to-end integration tests that drive the actual social-fetch
// and social-ledger binaries (via go build + exec) instead of
// calling into internal packages. Verifies the full data path:
//
//	social-fetch fetch <url>     →  HTTP GET against an httptest server
//	                            →  rendered output on stdout
//	(when SOCIAL_LEDGER=1)
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

// TestFetchExplicitlyDisabled verifies SOCIAL_LEDGER=0 wins
// over a present binary — the explicit off-switch beats the
// auto-detect default.
func TestFetchExplicitlyDisabled(t *testing.T) {
	sf, ledger := buildBinaries(t)
	srv := fakeArticle(t)
	defer srv.Close()
	dataDir := t.TempDir()

	cmd := exec.Command(sf, "fetch", srv.URL, "--no-comments")
	cmd.Env = append(os.Environ(),
		"SOCIAL_LEDGER=0",
		"SOCIAL_LEDGER_BIN="+ledger,
		"SOCIAL_LEDGER_DIR="+dataDir,
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

// TestFetchWithLedger verifies SOCIAL_LEDGER=1 routes the
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
		"SOCIAL_LEDGER=1",
		"SOCIAL_LEDGER_BIN="+ledger,
		"SOCIAL_LEDGER_DIR="+dataDir,
		// Skip the daemon path — a developer's machine may have
		// `social-ledger daemon run` up on :5557 from a previous
		// session, which would steal the ingest and write to its
		// own data dir rather than this test's tempdir.
		"SOCIAL_LEDGER_DAEMON_DISABLE=1",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("fetch: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "The Integration Test Article") {
		t.Errorf("expected article title in stdout, got:\n%s", out)
	}

	// SQLite db should exist under the project subdir (env path
	// always applies projects/<NAME>/, default project name is
	// "social_fetch"). See cmd/social-ledger/main.go's dataDir().
	projDir := filepath.Join(dataDir, "projects", "social_fetch")
	if _, err := os.Stat(filepath.Join(projDir, "ledger.db")); err != nil {
		t.Fatalf("ledger.db not created: %v", err)
	}
	// Markdown mirror tree should have the item somewhere under tree/.
	treeDir := filepath.Join(projDir, "tree")
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

	// `social-ledger article list` should report exactly 1 item.
	// Use SOCIAL_LEDGER_DIR (env path applies projects/<NAME>/)
	// instead of --data-dir (which would target the bare path
	// and miss the subdir).
	listCmd := exec.Command(ledger, "article", "list")
	listCmd.Env = append(os.Environ(), "SOCIAL_LEDGER_DIR="+dataDir)
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
// sets SOCIAL_LEDGER=1 but never installed social-ledger,
// the parent fetch still succeeds. The ledger failure shows up in
// the audit log only, never as a non-zero exit on the parent.
func TestFetchLedgerSurvivesMissingBinary(t *testing.T) {
	sf, _ := buildBinaries(t)
	srv := fakeArticle(t)
	defer srv.Close()

	cmd := exec.Command(sf, "fetch", srv.URL, "--no-comments")
	cmd.Env = append(os.Environ(),
		"SOCIAL_LEDGER=1",
		"SOCIAL_LEDGER_BIN=/nonexistent/social-ledger",
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
// SOCIAL_LEDGER unset + a discoverable binary auto-enables
// the ingest. This is the path most users will hit if they install
// both binaries — no env-var tinkering required.
func TestFetchAutoDetectEnabled(t *testing.T) {
	sf, ledger := buildBinaries(t)
	srv := fakeArticle(t)
	defer srv.Close()
	dataDir := t.TempDir()

	clean := []string{}
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "SOCIAL_LEDGER=") {
			clean = append(clean, kv)
		}
	}
	cmd := exec.Command(sf, "fetch", srv.URL, "--no-comments")
	cmd.Env = append(clean,
		"SOCIAL_LEDGER_BIN="+ledger,
		"SOCIAL_LEDGER_DIR="+dataDir,
		// Same reason as TestFetchWithLedger — skip any local
		// daemon that would write to a different data dir.
		"SOCIAL_LEDGER_DAEMON_DISABLE=1",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("fetch: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "The Integration Test Article") {
		t.Errorf("expected article title in output, got:\n%s", out)
	}
	// Env-driven path applies projects/<NAME>/ subdir.
	projDir := filepath.Join(dataDir, "projects", "social_fetch")
	if _, err := os.Stat(filepath.Join(projDir, "ledger.db")); err != nil {
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
		if !strings.HasPrefix(kv, "SOCIAL_LEDGER") && !strings.HasPrefix(kv, "PATH=") {
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

// TestInfluencerCRUD walks the full influencer lifecycle through
// the real social-ledger binary in subprocess (daemon-disabled)
// mode. Pins three things at once:
//
//   - The ledger.Get / ledger.Ingest subprocess fallback paths
//     find the same SQLite file (via env propagation, NOT --data-dir
//     forwarding — the latter would bypass project subdir
//     resolution).
//   - `article list --format json` round-trips through SQLite and
//     comes back with socials populated (the in-memory shape that
//     mergeForAdd writes is not what we read here; this proves the
//     post-persist shape works too).
//   - Subscribe / Unsubscribe / Remove all hit the right rows.
//
// Daemon-disabled because the daemon path is exercised in
// internal/ledger/daemon_test.go; this test is specifically for
// the subprocess fallback, which is what users without a running
// daemon hit (and what was broken before #69).
func TestInfluencerCRUD(t *testing.T) {
	_, ledger := buildBinaries(t)
	dataDir := t.TempDir()

	env := append(os.Environ(),
		"SOCIAL_LEDGER_DAEMON_DISABLE=1",
		"SOCIAL_LEDGER_DIR="+dataDir,
		"SOCIAL_LEDGER_BIN="+ledger,
	)

	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command(ledger, args...)
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return string(out)
	}

	// 1. Add — populates socials and topics.
	out := run("influencer", "add", "Andrej Karpathy",
		"--x", "karpathy", "--github", "karpathy",
		"--topics", "ai,research")
	if !strings.Contains(out, "slug=andrej-karpathy") {
		t.Fatalf("expected slug=andrej-karpathy in add output, got:\n%s", out)
	}

	// 2. List via the JSON-format article path. This is the
	//    subprocess fallback the influencers package uses.
	out = run("article", "list", "--source", "influencer", "--format", "json", "-n", "10")
	if !strings.Contains(out, `"karpathy"`) {
		t.Errorf("expected x handle 'karpathy' in JSON output, got:\n%s", out)
	}
	if !strings.Contains(out, `"ai"`) {
		t.Errorf("expected topic 'ai' in JSON output, got:\n%s", out)
	}

	// 3. Subscribe — adds an x channel follow scoped to ai.
	out = run("influencer", "subscribe", "Andrej Karpathy",
		"--platform", "x", "--topics", "ai")
	if !strings.Contains(out, "subscribed: Andrej Karpathy on x") {
		t.Errorf("expected subscribe confirmation, got:\n%s", out)
	}

	// 4. Show should reflect the follow.
	out = run("influencer", "show", "andrej-karpathy", "--format", "json")
	if !strings.Contains(out, `"follows"`) || !strings.Contains(out, `"platform":"x"`) {
		t.Errorf("expected follows[platform=x] in show output, got:\n%s", out)
	}

	// 5. Re-add with mastodon — should upsert without losing
	//    existing socials/topics. This is the upsert invariant.
	run("influencer", "add", "Andrej Karpathy",
		"--social", "mastodon=@k@hachyderm.io")
	out = run("influencer", "show", "andrej-karpathy", "--format", "json")
	for _, want := range []string{`"x":"karpathy"`, `"github":"karpathy"`, `"mastodon":"@k@hachyderm.io"`, `"ai"`, `"research"`} {
		if !strings.Contains(out, want) {
			t.Errorf("after upsert, expected %q in show output, got:\n%s", want, out)
		}
	}

	// 6. Unsubscribe — drops the x follow.
	out = run("influencer", "unsubscribe", "andrej-karpathy", "--platform", "x")
	if !strings.Contains(out, "unsubscribed") {
		t.Errorf("expected unsubscribe confirmation, got:\n%s", out)
	}

	// 7. List filtered by --has — should still find the entry
	//    via mastodon now that x's follow is gone (entry itself
	//    still has socials).
	out = run("influencer", "list", "--has", "mastodon")
	if !strings.Contains(out, "Andrej Karpathy") {
		t.Errorf("expected Andrej Karpathy in --has mastodon list, got:\n%s", out)
	}

	// 8. Remove — clean shutdown.
	out = run("influencer", "remove", "andrej-karpathy")
	if !strings.Contains(out, "removed") {
		t.Errorf("expected 'removed' in remove output, got:\n%s", out)
	}

	// 9. List after remove — should be empty.
	out = run("influencer", "list")
	if !strings.Contains(out, "no influencers matched") {
		t.Errorf("expected empty-list message, got:\n%s", out)
	}
}
