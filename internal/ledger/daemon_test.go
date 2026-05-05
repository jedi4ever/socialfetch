package ledger

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jedi4ever/social-skills/internal/ledger/item"
	"github.com/jedi4ever/social-skills/internal/ledger/store"
)

// startTestDaemon spins up a Daemon backed by a fresh temp SQLite
// store, returns a base URL the test can hit + a teardown closure.
// Used by every handler test below — keeps the per-test boilerplate
// small while still giving each test an isolated DB.
func startTestDaemon(t *testing.T) (baseURL string, stop func()) {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "ledger.db")

	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	d := &Daemon{
		DBPath:  dbPath,
		store:   st,
		startAt: time.Now(),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/status", d.handleStatus)
	mux.HandleFunc("/ingest", d.handleIngest)
	mux.HandleFunc("/forget", d.handleForget)
	mux.HandleFunc("/search", d.handleSearch)
	mux.HandleFunc("/get", d.handleGet)
	mux.HandleFunc("/content", d.handleContent)
	mux.HandleFunc("/list", d.handleList)
	mux.HandleFunc("/seen", d.handleSeen)
	mux.HandleFunc("/stats", d.handleStats)

	srv := httptest.NewServer(mux)
	return srv.URL, func() {
		srv.Close()
		_ = st.Close()
	}
}

// TestStatus is the smoke test — running daemon answers /status with
// the expected envelope shape.
func TestStatus(t *testing.T) {
	base, stop := startTestDaemon(t)
	defer stop()

	resp, err := http.Get(base + "/status")
	if err != nil {
		t.Fatalf("GET /status: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var st StatusResponse
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if st.DBPath == "" {
		t.Errorf("DBPath empty")
	}
	if st.UpSeconds < 0 {
		t.Errorf("UpSeconds negative: %d", st.UpSeconds)
	}
}

// TestIngestThenSeen covers the round trip: POST /ingest stores an
// item, GET /seen?url= reports it as seen with the right metadata.
// Locks in the URL→key resolution + the per-counter ticks.
func TestIngestThenSeen(t *testing.T) {
	base, stop := startTestDaemon(t)
	defer stop()

	it := item.Item{
		Source:    "linkedin",
		URL:       "https://www.linkedin.com/posts/jane",
		Title:     "Hello",
		Content:   "body text",
		FetchedAt: time.Now().UTC(),
	}
	body, _ := json.Marshal(IngestRequest{Items: []item.Item{it}})
	resp, err := http.Post(base+"/ingest", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /ingest: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("ingest %d: %s", resp.StatusCode, b)
	}
	var ingest IngestResponse
	_ = json.NewDecoder(resp.Body).Decode(&ingest)
	if ingest.Total != 1 || ingest.New != 1 {
		t.Errorf("ingest = %+v, want Total=1 New=1", ingest)
	}

	// Now /seen should report it.
	r2, err := http.Get(base + "/seen?url=" + it.URL)
	if err != nil {
		t.Fatalf("GET /seen: %v", err)
	}
	defer r2.Body.Close()
	var seen SeenResponse
	_ = json.NewDecoder(r2.Body).Decode(&seen)
	if !seen.Seen {
		t.Errorf("seen=false, want true; resp=%+v", seen)
	}
	if seen.Source != "linkedin" {
		t.Errorf("source = %q", seen.Source)
	}
	if seen.LastSeen == 0 {
		t.Errorf("LastSeen=0")
	}
}

// TestContent verifies the /content endpoint returns just the
// markdown body (text/markdown), not the full JSON envelope.
// MCP's daemon-mode envelope hands the agent this URL so it can
// read the body without filesystem access.
func TestContent(t *testing.T) {
	base, stop := startTestDaemon(t)
	defer stop()

	const body = "# Hello\n\nThis is the body."
	it := item.Item{
		Source:    "article",
		URL:       "https://example.com/post-1",
		Title:     "Post 1",
		Content:   body,
		FetchedAt: time.Now().UTC(),
	}
	bs, _ := json.Marshal(IngestRequest{Items: []item.Item{it}})
	if r, err := http.Post(base+"/ingest", "application/json", bytes.NewReader(bs)); err != nil {
		t.Fatalf("ingest: %v", err)
	} else {
		r.Body.Close()
	}

	r, err := http.Get(base + "/content?url=" + it.URL)
	if err != nil {
		t.Fatalf("GET /content: %v", err)
	}
	defer r.Body.Close()
	if r.StatusCode != 200 {
		t.Fatalf("status %d", r.StatusCode)
	}
	got, _ := io.ReadAll(r.Body)
	if !strings.Contains(string(got), "Hello") {
		t.Errorf("body = %q, want it to contain 'Hello'", got)
	}
}

// TestContent404 — unknown URL returns 404, not 200 with empty body.
// Locks in the contract so MCP's content_url can detect "not in
// ledger" via status code.
func TestContent404(t *testing.T) {
	base, stop := startTestDaemon(t)
	defer stop()

	r, err := http.Get(base + "/content?url=https://example.com/never-fetched")
	if err != nil {
		t.Fatalf("GET /content: %v", err)
	}
	defer r.Body.Close()
	if r.StatusCode != 404 {
		t.Errorf("status = %d, want 404", r.StatusCode)
	}
}

// TestForget covers POST /forget by URL.
func TestForget(t *testing.T) {
	base, stop := startTestDaemon(t)
	defer stop()

	it := item.Item{
		Source: "article",
		URL:    "https://example.com/forget-me",
		Title:  "Goodbye",
		// Content non-empty so Ingest doesn't reject it.
		Content:   "x",
		FetchedAt: time.Now().UTC(),
	}
	bs, _ := json.Marshal(IngestRequest{Items: []item.Item{it}})
	r0, _ := http.Post(base+"/ingest", "application/json", bytes.NewReader(bs))
	r0.Body.Close()

	body, _ := json.Marshal(ForgetRequest{URL: it.URL})
	r, err := http.Post(base+"/forget", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /forget: %v", err)
	}
	defer r.Body.Close()
	if r.StatusCode != 200 {
		b, _ := io.ReadAll(r.Body)
		t.Fatalf("forget status %d: %s", r.StatusCode, b)
	}
	var out ForgetResponse
	_ = json.NewDecoder(r.Body).Decode(&out)
	if !out.Deleted {
		t.Errorf("Deleted=false, want true")
	}

	// /seen should report not-seen now.
	r2, err := http.Get(base + "/seen?url=" + it.URL)
	if err != nil {
		t.Fatalf("GET /seen: %v", err)
	}
	defer r2.Body.Close()
	var s SeenResponse
	_ = json.NewDecoder(r2.Body).Decode(&s)
	if s.Seen {
		t.Errorf("still seen after forget")
	}
}

// TestStatsEmpty — /stats works on an empty store. Stats counts
// shouldn't error or hang when there are no items.
func TestStatsEmpty(t *testing.T) {
	base, stop := startTestDaemon(t)
	defer stop()

	r, err := http.Get(base + "/stats")
	if err != nil {
		t.Fatalf("GET /stats: %v", err)
	}
	defer r.Body.Close()
	if r.StatusCode != 200 {
		t.Fatalf("status %d", r.StatusCode)
	}
	var s store.Stats
	_ = json.NewDecoder(r.Body).Decode(&s)
	if s.Total != 0 {
		t.Errorf("Total = %d, want 0 on empty store", s.Total)
	}
}

// TestRequestValidation — bad-shape requests return 4xx with a
// readable error rather than 500. The daemon's contract is to
// fail-soft on malformed clients.
func TestRequestValidation(t *testing.T) {
	base, stop := startTestDaemon(t)
	defer stop()

	cases := []struct {
		name   string
		method string
		path   string
		body   string
		status int
	}{
		{"GET on POST-only ingest", "GET", "/ingest", "", 405},
		{"malformed JSON ingest", "POST", "/ingest", "{not-json}", 400},
		{"forget without key/url", "POST", "/forget", `{}`, 400},
		{"search without q", "GET", "/search", "", 400},
		{"seen without url", "GET", "/seen", "", 400},
		{"content without key/url", "GET", "/content", "", 400},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req, _ := http.NewRequest(c.method, base+c.path, strings.NewReader(c.body))
			r, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("do: %v", err)
			}
			defer r.Body.Close()
			if r.StatusCode != c.status {
				b, _ := io.ReadAll(r.Body)
				t.Errorf("status = %d, want %d (body: %s)", r.StatusCode, c.status, b)
			}
		})
	}
}

// Compile-time guard against renaming public types/fields in a way
// that breaks the wire contract — every JSON tag the daemon emits
// is part of its public API.
func TestEnvelopeShapesUnchanged(t *testing.T) {
	// If you rename a JSON tag here, you've broken every existing
	// client. Bump the API version FIRST.
	for _, want := range []string{
		`"items"`, `"total"`, `"new"`, `"updated"`, `"unchanged"`,
		`"seen"`, `"key"`, `"source"`, `"last_seen_at"`,
		`"deleted"`, `"up_seconds"`, `"db_path"`,
	} {
		if !strings.Contains(rawTypeJSON(t), want) {
			t.Errorf("envelope JSON tags missing %s", want)
		}
	}
}

// rawTypeJSON marshals one value of each public envelope type and
// concatenates so TestEnvelopeShapesUnchanged can scan for tag
// presence in a single string.
func rawTypeJSON(t *testing.T) string {
	t.Helper()
	bits := []any{
		IngestRequest{},
		IngestResponse{},
		ForgetRequest{Key: "k", URL: "u"},
		ForgetResponse{},
		SeenResponse{Seen: true, Key: "k", Source: "x", LastSeen: 1},
		StatusResponse{},
	}
	var b strings.Builder
	for _, v := range bits {
		raw, _ := json.Marshal(v)
		b.Write(raw)
	}
	return b.String()
}

// silence unused-import warnings if a future test refactor drops
// imports.
var _ = context.TODO
