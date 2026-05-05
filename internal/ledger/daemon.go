package ledger

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jedi4ever/social-skills/internal/ledger/item"
	"github.com/jedi4ever/social-skills/internal/ledger/store"
)

// DefaultDaemonPort is the loopback port `social-ledger daemon
// start` listens on. Picks 5557 to extend the social-fetch local-
// services range (5555 bridge, 5556 headless, 5557 ledger).
const DefaultDaemonPort = 5557

// Daemon is the long-lived HTTP wrapper around a single
// *store.Store. Lifecycle: Run() opens the SQLite DB once, serves
// HTTP requests against the same store handle, closes on ctx
// cancellation. The daemon owns the DB while it's up — clients
// (CLI / MCP / auto-ingest) talk to it over HTTP rather than
// opening the DB file directly. See "Two modes" in the project
// plan for the all-or-nothing rationale.
//
// Cheap to construct; the actual SQLite open happens in Run().
type Daemon struct {
	// DBPath is the full path to ledger.db. Empty defaults to
	// $SOCIAL_LEDGER_DIR/ledger.db at Run() time.
	DBPath string

	// Logf is the audit hook. Defaults to a no-op so tests don't
	// spam stderr; the CLI wires it to fmt.Fprintf(os.Stderr,...).
	Logf func(format string, a ...any)

	store        *store.Store
	startAt      time.Time
	totalIngest  atomic.Int64
	totalQueries atomic.Int64
	mu           sync.Mutex
	closed       bool

	// history holds the most recent N ingest/query events for the
	// monitor command. Mutex-guarded; small fixed-size ring.
	history   *eventRing
	historyMu sync.Mutex
}

// EventEntry is one row in the recent-events ring buffer surfaced
// via /status's History field. Used by `social-ledger daemon
// monitor` to render a live tail.
type EventEntry struct {
	At     time.Time `json:"at"`
	Kind   string    `json:"kind"` // "ingest" or "query"
	Detail string    `json:"detail,omitempty"`
	OK     bool      `json:"ok"`
}

// eventRing is a fixed-size ring of recent daemon events (32
// entries — same cap as the headless daemon's history). Older
// entries are overwritten; readers get a fresh slice in newest-
// first order.
type eventRing struct {
	entries []EventEntry
	cap     int
}

func newEventRing(cap int) *eventRing {
	return &eventRing{entries: make([]EventEntry, 0, cap), cap: cap}
}

func (r *eventRing) add(e EventEntry) {
	if len(r.entries) < r.cap {
		r.entries = append(r.entries, e)
		return
	}
	copy(r.entries, r.entries[1:])
	r.entries[len(r.entries)-1] = e
}

// snapshot returns the entries newest-first.
func (r *eventRing) snapshot() []EventEntry {
	out := make([]EventEntry, len(r.entries))
	for i, e := range r.entries {
		out[len(r.entries)-1-i] = e
	}
	return out
}

// recordEvent appends to the history ring under the mutex. Cheap
// — one allocation per event, no locking on the hot status path
// since /status only reads.
func (d *Daemon) recordEvent(kind, detail string, ok bool) {
	if d.history == nil {
		return // pre-Run() ingest path or test setup
	}
	d.historyMu.Lock()
	defer d.historyMu.Unlock()
	d.history.add(EventEntry{
		At: time.Now(), Kind: kind, Detail: detail, OK: ok,
	})
}

// Run opens the SQLite store and serves the HTTP API at addr until
// ctx is cancelled or ListenAndServe errors. Always closes the
// store before returning — leaving SQLite handles open after a
// crash is the worst-case bug here.
func (d *Daemon) Run(ctx context.Context, addr string) error {
	if d.Logf == nil {
		d.Logf = func(string, ...any) {}
	}
	if d.DBPath == "" {
		return errors.New("ledger daemon: DBPath is required")
	}

	st, err := store.Open(d.DBPath)
	if err != nil {
		return fmt.Errorf("ledger daemon: open %s: %w", d.DBPath, err)
	}
	d.store = st
	d.startAt = time.Now()
	d.history = newEventRing(32)

	mux := http.NewServeMux()
	mux.HandleFunc("/status", d.handleStatus)
	mux.HandleFunc("/health", d.handleHealth)
	mux.HandleFunc("/monitor", d.handleMonitor)
	mux.HandleFunc("/ingest", d.handleIngest)
	mux.HandleFunc("/forget", d.handleForget)
	mux.HandleFunc("/search", d.handleSearch)
	mux.HandleFunc("/get", d.handleGet)
	mux.HandleFunc("/content", d.handleContent)
	mux.HandleFunc("/list", d.handleList)
	mux.HandleFunc("/seen", d.handleSeen)
	mux.HandleFunc("/stats", d.handleStats)
	mux.HandleFunc("/screenshots/", d.handleScreenshots)
	mux.HandleFunc("/shutdown", d.handleShutdown)

	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	defer d.shutdown()
	d.Logf("listening on %s, db=%s", addr, d.DBPath)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// shutdown closes the store. Idempotent.
func (d *Daemon) shutdown() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return
	}
	d.closed = true
	if d.store != nil {
		_ = d.store.Close()
	}
}

// ----- request / response shapes -----

// IngestRequest is the body of POST /ingest. Items are an array
// rather than JSONL so the body is well-formed JSON (callers
// who want JSONL can wrap each line themselves; the `social-fetch`
// auto-ingest path uses one POST per fetch so a flat array is the
// natural shape).
type IngestRequest struct {
	Items []item.Item `json:"items"`
}

// IngestResponse mirrors the per-item outcome the store returns.
// New + Updated counts let callers reason about churn without
// re-querying.
type IngestResponse struct {
	Total     int `json:"total"`
	New       int `json:"new"`
	Updated   int `json:"updated"`
	Unchanged int `json:"unchanged"`
}

// ForgetRequest body shape for DELETE-equivalent: identify by key
// (preferred) or url (URL gets resolved to key server-side).
type ForgetRequest struct {
	Key string `json:"key,omitempty"`
	URL string `json:"url,omitempty"`
}

type ForgetResponse struct {
	Deleted bool `json:"deleted"`
}

// SeenResponse is the answer to "have we ingested this URL?".
// Includes the key + last-seen timestamp for cache invalidation
// decisions on the client side.
type SeenResponse struct {
	Seen     bool   `json:"seen"`
	Key      string `json:"key,omitempty"`
	URL      string `json:"url,omitempty"`
	Source   string `json:"source,omitempty"`
	LastSeen int64  `json:"last_seen_at,omitempty"`
}

// StatusResponse is the daemon health answer. Mirrors the shape
// of the headless daemon's status — uptime, query counts, db
// path so monitoring scripts know which DB is active. History
// carries the last 32 ingest/query events so the monitor command
// can render a live tail without a separate /events stream.
type StatusResponse struct {
	UpSeconds int64        `json:"up_seconds"`
	DBPath    string       `json:"db_path"`
	Ingests   int64        `json:"ingests_total"`
	Queries   int64        `json:"queries_total"`
	History   []EventEntry `json:"history,omitempty"`
}

// ----- handlers -----

func (d *Daemon) handleStatus(w http.ResponseWriter, _ *http.Request) {
	var hist []EventEntry
	if d.history != nil {
		d.historyMu.Lock()
		hist = d.history.snapshot()
		d.historyMu.Unlock()
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(StatusResponse{
		UpSeconds: int64(time.Since(d.startAt).Seconds()),
		DBPath:    d.DBPath,
		Ingests:   d.totalIngest.Load(),
		Queries:   d.totalQueries.Load(),
		History:   hist,
	})
}

func (d *Daemon) handleIngest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 10*1024*1024))
	if err != nil {
		http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
		return
	}
	var req IngestRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "decode body: "+err.Error(), http.StatusBadRequest)
		return
	}
	resp := IngestResponse{Total: len(req.Items)}
	for _, it := range req.Items {
		res, err := d.store.Ingest(it)
		if err != nil {
			d.Logf("ingest: %v (url=%s)", err, it.URL)
			http.Error(w, fmt.Sprintf("ingest: %v", err), http.StatusInternalServerError)
			return
		}
		switch res {
		case store.IngestNew:
			resp.New++
		case store.IngestUpdated:
			resp.Updated++
		case store.IngestUnchanged:
			resp.Unchanged++
		}
	}
	d.totalIngest.Add(int64(len(req.Items)))
	// Record one event per item URL — easier to read in the
	// monitor view than a single batched line.
	for _, it := range req.Items {
		d.recordEvent("ingest", it.URL, true)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func (d *Daemon) handleForget(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 4096))
	if err != nil {
		http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
		return
	}
	var req ForgetRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "decode body: "+err.Error(), http.StatusBadRequest)
		return
	}
	key := req.Key
	if key == "" && req.URL != "" {
		// Resolve URL → key by looking it up first.
		if hit, _ := d.store.LookupMetaByURL(req.URL); hit != nil {
			key = hit.Key
		}
	}
	if key == "" {
		http.Error(w, "key or known url required", http.StatusBadRequest)
		return
	}
	deleted, err := d.store.Forget(key)
	if err != nil {
		http.Error(w, "forget: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(ForgetResponse{Deleted: deleted})
}

func (d *Daemon) handleSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		http.Error(w, "q is required", http.StatusBadRequest)
		return
	}
	limit := atoiOr(r.URL.Query().Get("limit"), 25)
	items, err := d.store.Search(q, limit)
	if err != nil {
		http.Error(w, "search: "+err.Error(), http.StatusInternalServerError)
		return
	}
	d.totalQueries.Add(1)
	d.recordEvent("query", "search "+q, true)
	writeJSONItems(w, items)
}

func (d *Daemon) handleGet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	key := r.URL.Query().Get("key")
	url := r.URL.Query().Get("url")
	if key == "" && url == "" {
		http.Error(w, "key or url required", http.StatusBadRequest)
		return
	}
	if key == "" {
		hit, err := d.store.LookupMetaByURL(url)
		if err != nil {
			http.Error(w, "lookup: "+err.Error(), http.StatusInternalServerError)
			return
		}
		if hit == nil {
			http.NotFound(w, r)
			return
		}
		key = hit.Key
	}
	it, err := d.store.Get(key)
	if err != nil {
		http.Error(w, "get: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if it == nil {
		http.NotFound(w, r)
		return
	}
	d.totalQueries.Add(1)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(it)
}

// handleContent returns the raw body of an item as text/markdown.
// Used by MCP when running in daemon-mode: instead of writing a
// temp file and returning a local path the remote agent can't
// read, MCP returns `content_url` pointing here. The agent's
// fetch / Read tool gets the body over HTTP.
//
// Identify the item by `key` (preferred) or `url`. Returns 404
// when nothing matches.
func (d *Daemon) handleContent(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	key := r.URL.Query().Get("key")
	url := r.URL.Query().Get("url")
	if key == "" && url == "" {
		http.Error(w, "key or url required", http.StatusBadRequest)
		return
	}
	if key == "" {
		hit, err := d.store.LookupMetaByURL(url)
		if err != nil {
			http.Error(w, "lookup: "+err.Error(), http.StatusInternalServerError)
			return
		}
		if hit == nil {
			http.NotFound(w, r)
			return
		}
		key = hit.Key
	}
	it, err := d.store.Get(key)
	if err != nil {
		http.Error(w, "get: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if it == nil {
		http.NotFound(w, r)
		return
	}
	d.totalQueries.Add(1)
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	_, _ = io.WriteString(w, it.Content)
}

func (d *Daemon) handleList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	opts := store.ListOpts{
		Source: r.URL.Query().Get("source"),
		Limit:  atoiOr(r.URL.Query().Get("limit"), 50),
	}
	if since := r.URL.Query().Get("since"); since != "" {
		// Accept Unix epoch seconds OR RFC3339. Fail-soft: bad
		// values just leave Since zero so the result is unfiltered
		// rather than 400.
		if n, err := strconv.ParseInt(since, 10, 64); err == nil {
			opts.Since = time.Unix(n, 0)
		} else if t, err := time.Parse(time.RFC3339, since); err == nil {
			opts.Since = t
		}
	}
	items, err := d.store.List(opts)
	if err != nil {
		http.Error(w, "list: "+err.Error(), http.StatusInternalServerError)
		return
	}
	d.totalQueries.Add(1)
	writeJSONItems(w, items)
}

func (d *Daemon) handleSeen(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	url := r.URL.Query().Get("url")
	if url == "" {
		http.Error(w, "url required", http.StatusBadRequest)
		return
	}
	hit, err := d.store.LookupMetaByURL(url)
	if err != nil {
		http.Error(w, "lookup: "+err.Error(), http.StatusInternalServerError)
		return
	}
	d.totalQueries.Add(1)
	resp := SeenResponse{Seen: hit != nil, URL: url}
	if hit != nil {
		resp.Key = hit.Key
		resp.Source = hit.Source
		resp.LastSeen = hit.FetchedAt.Unix()
	}
	d.recordEvent("query", "seen "+url, true)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func (d *Daemon) handleStats(w http.ResponseWriter, _ *http.Request) {
	st, err := d.store.Stats()
	if err != nil {
		http.Error(w, "stats: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(st)
}

// handleScreenshots multiplexes GET (serve a stored PNG) and POST
// (upload a new PNG) on /screenshots/. POST replies with the
// filename + URL the GET path will serve from. Filenames live in
// <data_dir>/screenshots/ — the daemon owns this directory; clients
// (MCP server, CLI on a different host/container) only know the
// HTTP API.
//
// POST shape:
//
//	POST /screenshots                  body: raw PNG bytes
//	                                   query: ?name=<file>.png (optional)
//	                                   header: Content-Type: image/png
//	200 application/json               {"filename":"...", "url":"...", "bytes":N}
//
// GET shape:
//
//	GET /screenshots/<filename>        200 image/png | 404
//
// The split lets cross-container deployments work: the MCP server
// captures via the headless daemon (one container), POSTs the PNG
// to the ledger daemon (another container), and returns the URL
// to the agent — no shared volume needed.
func (d *Daemon) handleScreenshots(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		d.serveScreenshot(w, r)
	case http.MethodPost:
		d.uploadScreenshot(w, r)
	default:
		http.Error(w, "GET or POST only", http.StatusMethodNotAllowed)
	}
}

// serveScreenshot returns the bytes of a stored PNG. Path safety:
// filenames are restricted to alphanumeric + dot + dash + underscore,
// must end in .png, and pass through filepath.Base so any `..`
// component is stripped before the regex check.
func (d *Daemon) serveScreenshot(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/screenshots/")
	// "/screenshots" with no trailing path is a usage hint, not a
	// served file.
	if name == "" {
		http.Error(w, "filename required: GET /screenshots/<file>.png", http.StatusBadRequest)
		return
	}
	name = filepath.Base(name)
	if !validScreenshotFilename(name) {
		http.Error(w, "invalid filename", http.StatusBadRequest)
		return
	}
	dir := d.screenshotsDir()
	full := filepath.Join(dir, name)
	f, err := os.Open(full)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
	d.totalQueries.Add(1)
	if r.Method == http.MethodHead {
		return
	}
	_, _ = io.Copy(w, f)
}

// uploadScreenshot accepts PNG bytes in the request body and writes
// them to the daemon's screenshots dir. Filename is taken from the
// `name` query when valid, otherwise auto-generated. Replies with
// the canonical URL the GET path will serve from.
//
// Caller responsibility: send actual PNG bytes. We sniff the magic
// header to refuse obvious mismatches (HTML error page, JSON, etc.)
// but don't fully decode — the goal is to fail fast on
// content-type confusion, not to validate every PNG byte.
func (d *Daemon) uploadScreenshot(w http.ResponseWriter, r *http.Request) {
	// Bound the upload size — typical full-page screenshot is
	// ~50-500 KB; cap at 16 MB so a runaway client can't exhaust
	// the daemon's memory.
	const maxUpload = 16 * 1024 * 1024
	body, err := io.ReadAll(io.LimitReader(r.Body, maxUpload+1))
	if err != nil {
		http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if len(body) > maxUpload {
		http.Error(w, "screenshot too large (max 16 MB)", http.StatusRequestEntityTooLarge)
		return
	}
	if len(body) < 8 || string(body[:8]) != "\x89PNG\r\n\x1a\n" {
		http.Error(w, "body is not a PNG (magic bytes mismatch)", http.StatusBadRequest)
		return
	}

	dir := d.screenshotsDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		http.Error(w, "mkdir: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Honour client-supplied name if it passes validation;
	// otherwise auto-generate via os.CreateTemp so we don't
	// collide with existing entries.
	requested := strings.TrimSpace(r.URL.Query().Get("name"))
	var path string
	if requested != "" {
		base := filepath.Base(requested)
		if !validScreenshotFilename(base) {
			http.Error(w, "invalid name", http.StatusBadRequest)
			return
		}
		path = filepath.Join(dir, base)
		if err := os.WriteFile(path, body, 0o644); err != nil {
			http.Error(w, "write: "+err.Error(), http.StatusInternalServerError)
			return
		}
	} else {
		f, err := os.CreateTemp(dir, "social-fetch-screenshot-*.png")
		if err != nil {
			http.Error(w, "create: "+err.Error(), http.StatusInternalServerError)
			return
		}
		path = f.Name()
		if _, err := f.Write(body); err != nil {
			f.Close()
			os.Remove(path)
			http.Error(w, "write: "+err.Error(), http.StatusInternalServerError)
			return
		}
		f.Close()
	}

	filename := filepath.Base(path)
	d.recordEvent("screenshot", filename, true)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"filename": filename,
		"url":      "/screenshots/" + filename,
		"bytes":    len(body),
	})
}

// screenshotsDir derives the per-project screenshots dir from
// the daemon's DBPath. <project_root>/screenshots so the GET / POST
// handlers agree on the same location regardless of how the daemon
// was started.
func (d *Daemon) screenshotsDir() string {
	return filepath.Join(filepath.Dir(d.DBPath), "screenshots")
}

// validScreenshotFilename whitelists the names handleScreenshots
// will serve. Pattern matches what writeScreenshotPNG produces
// (social-fetch-screenshot-<slug>-<unix>.png) plus the looser CLI
// `-o <path>` form that callers might choose. No path components,
// no dotfiles, no extensions other than .png.
var screenshotFilenameRE = regexp.MustCompile(`^[a-zA-Z0-9._-]+\.png$`)

func validScreenshotFilename(s string) bool {
	if s == "" || strings.HasPrefix(s, ".") {
		return false
	}
	return screenshotFilenameRE.MatchString(s)
}

// handleHealth is a sub-millisecond liveness probe used by
// container HEALTHCHECK directives. No JSON parsing / no allocations
// — just `200 OK\nok\n`. Anything more elaborate goes through
// /status; /monitor renders the same data text-formatted.
func (d *Daemon) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, "ok\n")
}

// handleMonitor renders the daemon's status + recent events as a
// human-readable text page. Designed for `curl … | less` style
// operator checks inside containers where the JSON /status output
// is unwieldy without jq. Same data the CLI `social-ledger daemon
// monitor` shows, but as a snapshot rather than a live tail.
func (d *Daemon) handleMonitor(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")

	up := time.Since(d.startAt).Round(time.Second)
	fmt.Fprintf(w, "social-ledger daemon\n")
	fmt.Fprintf(w, "  db          %s\n", d.DBPath)
	fmt.Fprintf(w, "  uptime      %s\n", up)
	fmt.Fprintf(w, "  ingests     %d\n", d.totalIngest.Load())
	fmt.Fprintf(w, "  queries     %d\n", d.totalQueries.Load())

	if d.history == nil {
		return
	}
	d.historyMu.Lock()
	hist := d.history.snapshot()
	d.historyMu.Unlock()

	if len(hist) == 0 {
		fmt.Fprintln(w, "\nrecent events: (none)")
		return
	}
	fmt.Fprintln(w, "\nrecent events (newest first):")
	for _, e := range hist {
		ok := "ok"
		if !e.OK {
			ok = "FAIL"
		}
		fmt.Fprintf(w, "  %s  %-10s %-4s  %s\n",
			e.At.Format("15:04:05"), e.Kind, ok, e.Detail)
	}
}

func (d *Daemon) handleShutdown(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	w.WriteHeader(http.StatusAccepted)
	go func() {
		time.Sleep(100 * time.Millisecond)
		d.shutdown()
	}()
}

// ----- small helpers -----

func atoiOr(s string, fallback int) int {
	if s == "" {
		return fallback
	}
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return fallback
	}
	return n
}

// writeJSONItems writes a JSON array of items. We always emit `[]`
// for empty results rather than `null` so callers can iterate
// without nil-checks.
func writeJSONItems(w http.ResponseWriter, items []item.Item) {
	w.Header().Set("Content-Type", "application/json")
	if items == nil {
		items = []item.Item{}
	}
	_ = json.NewEncoder(w).Encode(items)
}
