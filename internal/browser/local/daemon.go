// Package local runs an in-process chromedp browser pool and serves
// the same /fetch /screenshot /status /health /monitor HTTP surface
// that internal/browser/daemon.go (the proxy daemon) serves.
//
// Used by `social-browser daemon start --provider local`. Replaces
// the chromedp pool that previously lived in
// internal/render/headless/daemon.go; identical lifecycle (slot
// pool, recycle-after-N, fetch ring buffer) but rehoused here so
// social-fetch no longer needs to link chromedp.
package local

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"

	"github.com/jedi4ever/social-skills/internal/render/headless"
)

// DefaultPoolSize is how many warm browsers the daemon keeps ready
// when no flag override is set. Four matches `social-fetch fetch -j`'s
// default parallelism so a typical batch fetch (or 4 concurrent MCP
// tool calls) doesn't queue. Memory cost is ~120-160 MB total at
// rest; small enough on any modern machine, large enough that batch
// fetches don't serialise.
const DefaultPoolSize = 4

// DefaultRecycleAfter is how many fetches a single browser handles
// before the daemon kills + respawns it. Identity rotation matters
// for anti-bot — sites like Medium fingerprint per browser session
// and degrade responses after repeated visits. 50 fetches is a
// rough sweet spot: enough to amortise the ~2s warmup cost, low
// enough that fingerprint accumulation stays bounded.
const DefaultRecycleAfter = 50

// Daemon is the long-lived chromedp pool exposed over HTTP.
// Lifecycle: pool initialised on Run(); each /fetch request
// acquires a slot, runs the chromedp navigation, releases the slot.
// Slots that hit RecycleAfter get torn down and respawned in place.
//
// Cheap to construct — the actual browser processes are launched
// only when Run() starts. Tests can construct a Daemon, hand it a
// listener, and exercise the HTTP surface without paying for real
// Chrome unless they explicitly call Run.
type Daemon struct {
	// PoolSize is the number of warm browsers maintained. <=0 falls
	// back to DefaultPoolSize.
	PoolSize int

	// RecycleAfter is how many fetches a single browser handles
	// before being torn down + respawned. <=0 disables recycling.
	RecycleAfter int

	// Options overrides what each browser is launched with.
	// Empty fields fall back to headless.DefaultOptions equivalents.
	// Cookies are *not* honoured on the daemon today — daemon mode
	// is anonymous-only.
	Options headless.Options

	// Logf is the audit hook. Defaults to a no-op so tests don't
	// spam stderr; the CLI wires it to fmt.Fprintf(os.Stderr,...).
	Logf func(format string, a ...any)

	pool   chan *slot
	closed bool
	mu     sync.Mutex

	// state tracks per-slot live status (busy/free, current URL,
	// last-fetch result) and a ring buffer of recent fetches for
	// the monitor command. Mutex-guarded because /status reads
	// concurrently with /fetch handlers updating.
	state   *daemonState
	stateMu sync.Mutex
	startAt time.Time
}

// daemonState holds the runtime telemetry /status surfaces. Updated
// by handleFetch around each request; read by handleStatus. Kept
// inside the daemon (not on the slot itself) so /status can read
// state for slots that are CURRENTLY OUT of the pool channel — the
// slot.go field would be inaccessible without acquiring the slot.
type daemonState struct {
	slots   map[int]*slotState // keyed by slot.id
	history *fetchRing
}

// slotState is the per-browser snapshot. Free=true means the slot is
// in the pool channel and available; Free=false means a /fetch
// handler currently owns it. CurrentURL / BusySince are populated
// only while the slot is busy. LastFetch* persist across fetches
// for "what did this slot do most recently" reporting.
type slotState struct {
	ID            int       `json:"id"`
	Free          bool      `json:"free"`
	UsesRemaining int       `json:"uses_remaining"`
	TotalFetches  int       `json:"total_fetches"`
	CurrentURL    string    `json:"current_url,omitempty"`
	BusySince     time.Time `json:"busy_since,omitempty"`
	LastURL       string    `json:"last_url,omitempty"`
	LastAt        time.Time `json:"last_at,omitempty"`
	LastOK        bool      `json:"last_ok,omitempty"`
	LastDurMS     int64     `json:"last_dur_ms,omitempty"`
}

// fetchEntry is one row in the recent-fetches ring buffer.
type fetchEntry struct {
	SlotID  int       `json:"slot_id"`
	URL     string    `json:"url"`
	StartAt time.Time `json:"start_at"`
	DurMS   int64     `json:"dur_ms"`
	OK      bool      `json:"ok"`
	Err     string    `json:"err,omitempty"`
}

// fetchRing is a fixed-size ring of recent fetches. 32 entries is
// enough to populate a `monitor` view; older entries are
// overwritten. Newest-first ordering on read so the monitor doesn't
// have to reverse.
type fetchRing struct {
	entries []fetchEntry
	cap     int
}

func newFetchRing(cap int) *fetchRing {
	return &fetchRing{entries: make([]fetchEntry, 0, cap), cap: cap}
}

func (r *fetchRing) add(e fetchEntry) {
	if len(r.entries) < r.cap {
		r.entries = append(r.entries, e)
		return
	}
	// Shift out oldest (entries[0]). Cheap at cap=32; a circular
	// index would be faster but readable code wins here.
	copy(r.entries, r.entries[1:])
	r.entries[len(r.entries)-1] = e
}

// snapshot returns the entries in newest-first order. Callers get
// a fresh slice — safe to release the mutex before serialising.
func (r *fetchRing) snapshot() []fetchEntry {
	out := make([]fetchEntry, len(r.entries))
	for i, e := range r.entries {
		out[len(r.entries)-1-i] = e
	}
	return out
}

// slot holds one browser's chromedp contexts plus a usage counter.
// Slots are passed by pointer through the pool channel; only one
// goroutine ever owns a slot at a time (channel semantics enforce
// this), so the inner fields don't need locking.
type slot struct {
	id            int
	allocCtx      context.Context
	cancelAlloc   context.CancelFunc
	browserCtx    context.Context
	cancelBrowser context.CancelFunc
	usesRemaining int
	dead          bool // slot was lost to a panic/error mid-fetch
}

// Run initialises the pool, starts the HTTP server, and blocks
// until ctx is cancelled or ListenAndServe returns. Always tears
// down every slot before returning, even on error — leaving Chrome
// processes orphaned is the worst-case bug here.
func (d *Daemon) Run(ctx context.Context, addr string) error {
	if d.PoolSize <= 0 {
		d.PoolSize = DefaultPoolSize
	}
	if d.RecycleAfter < 0 {
		d.RecycleAfter = 0
	}
	if d.Logf == nil {
		d.Logf = func(string, ...any) {}
	}

	d.startAt = time.Now()
	d.state = &daemonState{
		slots:   make(map[int]*slotState, d.PoolSize),
		history: newFetchRing(32),
	}
	d.pool = make(chan *slot, d.PoolSize)
	for i := 0; i < d.PoolSize; i++ {
		s, err := d.newSlot(ctx, i)
		if err != nil {
			d.shutdown()
			return fmt.Errorf("init slot %d: %w", i, err)
		}
		d.state.slots[i] = &slotState{ID: i, Free: true, UsesRemaining: s.usesRemaining}
		d.pool <- s
	}
	d.Logf("pool ready: size=%d recycle_after=%d", d.PoolSize, d.RecycleAfter)

	mux := http.NewServeMux()
	mux.HandleFunc("/fetch", d.handleFetch)
	mux.HandleFunc("/screenshot", d.handleScreenshot)
	mux.HandleFunc("/status", d.handleStatus)
	mux.HandleFunc("/health", d.handleHealth)
	mux.HandleFunc("/monitor", d.handleMonitor)
	mux.HandleFunc("/shutdown", d.handleShutdown)

	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}

	// Cancel the listener when ctx fires so SIGINT propagates.
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	defer d.shutdown()
	d.Logf("listening on %s", addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// newSlot builds one warm browser slot. The chromedp contexts stay
// live until the slot is recycled or the daemon shuts down — every
// /fetch reuses them by creating a fresh tab via chromedp.NewContext
// with the slot's browserCtx as parent.
func (d *Daemon) newSlot(parent context.Context, id int) (*slot, error) {
	opts := d.Options
	def := headless.DefaultOptions
	if opts.UserAgent == "" {
		opts.UserAgent = def.UserAgent
	}
	if opts.Locale == "" {
		opts.Locale = def.Locale
	}
	if opts.Timezone == "" {
		opts.Timezone = def.Timezone
	}
	if opts.ViewportWidth == 0 {
		opts.ViewportWidth = def.ViewportWidth
	}
	if opts.ViewportHeight == 0 {
		opts.ViewportHeight = def.ViewportHeight
	}
	if opts.Settle == 0 {
		opts.Settle = def.Settle
	}
	opts.Headless = true // daemon never wants a visible window

	allocOpts := buildAllocatorOpts(opts)
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(parent, allocOpts...)
	browserCtx, cancelBrowser := chromedp.NewContext(allocCtx)

	// Force Chrome launch + warm the page lifecycle by navigating
	// to about:blank and waiting for the body to be ready. A bare
	// chromedp.Run(browserCtx) launches Chrome but doesn't open or
	// navigate any tab — and an untouched session breaks
	// chromedp.CaptureScreenshot when it's the first action on a
	// fresh tab inside the slot (reproducible in containerised
	// Chromium: /fetch as the first request works and subsequent
	// /screenshot calls work; /screenshot as the first request hangs
	// until the per-fetch deadline). The throwaway about:blank
	// navigate moves the slot's browser past whatever lazy-init
	// step is missing; WaitReady ensures we don't return until the
	// session is actually ready, otherwise the first user request
	// races the load and the failure mode comes back intermittently.
	// Force Chrome launch with a no-op Run. The slot's tab is then
	// reused across all /fetch and /screenshot handlers (no
	// per-request chromedp.NewContext) so the tab is never "fresh"
	// for a screenshot — the first request's Navigate primes the
	// page lifecycle and subsequent screenshots work. Per-request
	// new tabs hung the first /screenshot in containerised Chromium
	// until the per-fetch deadline.
	if err := chromedp.Run(browserCtx); err != nil {
		cancelBrowser()
		cancelAlloc()
		return nil, fmt.Errorf("chrome launch: %w", err)
	}

	uses := d.RecycleAfter
	if uses <= 0 {
		// 0 / negative = no recycling. Use a sentinel that never
		// hits 0 (we just ignore RecycleAfter when 0).
		uses = -1
	}

	return &slot{
		id:            id,
		allocCtx:      allocCtx,
		cancelAlloc:   cancelAlloc,
		browserCtx:    browserCtx,
		cancelBrowser: cancelBrowser,
		usesRemaining: uses,
	}, nil
}

// recycle tears down a slot's browser and respawns a fresh one in
// place. Called when usesRemaining hits 0 OR when a fetch fails
// catastrophically (slot.dead=true). Same id so /status output
// stays stable.
func (d *Daemon) recycle(parent context.Context, s *slot) (*slot, error) {
	d.Logf("recycling slot %d", s.id)
	s.cancelBrowser()
	s.cancelAlloc()
	return d.newSlot(parent, s.id)
}

// fetchRequest / fetchResponse are the HTTP-level shapes. Kept
// minimal — URL in, HTML+finalURL+engine out. Errors come back as
// HTTP 5xx with a plain-text body so curl debugging stays readable.
type fetchRequest struct {
	URL string `json:"url"`
}

type fetchResponse struct {
	HTML     string `json:"html"`
	FinalURL string `json:"final_url"`
	Engine   string `json:"engine"`
}

func (d *Daemon) handleFetch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 4096))
	if err != nil {
		http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
		return
	}
	var req fetchRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "decode body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.URL) == "" {
		http.Error(w, "url is required", http.StatusBadRequest)
		return
	}

	// Acquire a slot. Block on the channel — if all slots are busy
	// the request waits. This naturally caps daemon-side concurrency
	// at PoolSize without any per-request thread management.
	var s *slot
	select {
	case s = <-d.pool:
	case <-r.Context().Done():
		http.Error(w, "client cancelled", http.StatusRequestTimeout)
		return
	}

	// Mark slot busy so /status / monitor can report what's running.
	startAt := time.Now()
	d.markBusy(s.id, req.URL, startAt)

	// Optional per-call settle override — used by callers that
	// want a longer JS-hydration wait without restarting the
	// daemon. Empty / unparseable → daemon's default.
	settleOverride := time.Duration(0)
	if v := strings.TrimSpace(r.URL.Query().Get("settle")); v != "" {
		if d2, err := time.ParseDuration(v); err == nil && d2 > 0 {
			settleOverride = d2
		}
	}

	html, finalURL, fetchErr := d.runFetch(r.Context(), s, req.URL, settleOverride)
	dur := time.Since(startAt)

	// Release: replace or recycle. Always returns a slot to the
	// pool so the next request can proceed.
	released := d.releaseSlot(s, fetchErr != nil)
	// Update state BEFORE returning the slot — otherwise a fast
	// follow-up /fetch could grab the slot and mark it busy
	// while our "free" update is still racing.
	d.markFree(released, req.URL, startAt, dur, fetchErr)
	d.pool <- released

	if fetchErr != nil {
		d.Logf("slot %d: fetch %s failed: %v", s.id, req.URL, fetchErr)
		http.Error(w, fetchErr.Error(), http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(fetchResponse{
		HTML:     html,
		FinalURL: finalURL,
		Engine:   "chromedp",
	})
}

// markBusy records that slot id is starting a fetch of url at
// startAt. Updates the per-slot state map; called before the
// chromedp navigation starts.
func (d *Daemon) markBusy(id int, url string, startAt time.Time) {
	d.stateMu.Lock()
	defer d.stateMu.Unlock()
	s, ok := d.state.slots[id]
	if !ok {
		return
	}
	s.Free = false
	s.CurrentURL = url
	s.BusySince = startAt
}

// markFree updates the per-slot state after a fetch completes,
// records the entry in the recent-history ring, and ticks
// TotalFetches. Called before the slot is returned to the pool —
// see handleFetch for the ordering rationale.
func (d *Daemon) markFree(s *slot, url string, startAt time.Time, dur time.Duration, fetchErr error) {
	d.stateMu.Lock()
	defer d.stateMu.Unlock()
	st, ok := d.state.slots[s.id]
	if !ok {
		return
	}
	st.Free = true
	st.CurrentURL = ""
	st.BusySince = time.Time{}
	st.UsesRemaining = s.usesRemaining
	st.TotalFetches++
	st.LastURL = url
	st.LastAt = startAt
	st.LastDurMS = dur.Milliseconds()
	st.LastOK = fetchErr == nil

	entry := fetchEntry{
		SlotID:  s.id,
		URL:     url,
		StartAt: startAt,
		DurMS:   dur.Milliseconds(),
		OK:      fetchErr == nil,
	}
	if fetchErr != nil {
		entry.Err = fetchErr.Error()
	}
	d.state.history.add(entry)
}

// releaseSlot decides whether to keep the slot or recycle it.
// Recycle when: usesRemaining hit 0 OR the fetch errored (slot may
// be in a broken state). Returns the slot to push back into the
// pool — same one when keeping, fresh one when recycling.
func (d *Daemon) releaseSlot(s *slot, errored bool) *slot {
	if s.usesRemaining > 0 {
		s.usesRemaining--
	}
	shouldRecycle := errored || s.usesRemaining == 0
	if !shouldRecycle {
		return s
	}
	d.mu.Lock()
	closed := d.closed
	d.mu.Unlock()
	if closed {
		// Don't try to spawn a fresh browser during shutdown — the
		// pool channel is being drained; just return the (now dead)
		// slot so the receiver can finalise.
		s.dead = true
		return s
	}
	fresh, err := d.recycle(context.Background(), s)
	if err != nil {
		d.Logf("slot %d: recycle failed: %v", s.id, err)
		s.dead = true
		return s
	}
	// Reset state's UsesRemaining to the fresh browser's counter so
	// /status reflects the recycle.
	d.stateMu.Lock()
	if st, ok := d.state.slots[fresh.id]; ok {
		st.UsesRemaining = fresh.usesRemaining
	}
	d.stateMu.Unlock()
	return fresh
}

// runFetch is the actual chromedp navigate+grab. Reuses
// slot.browserCtx instead of spinning up a fresh allocator per
// call. A child context with chromedp.NewContext(slot.browserCtx)
// gives us a fresh tab inside the warm browser.
//
// settleOverride > 0 replaces the daemon's default settle for
// this call. Used by callers retrying a thin-content fetch
// (article fetcher does this automatically when the first
// attempt's body is suspiciously short — likely a JS-rendered
// SPA that needed more hydration time).
func (d *Daemon) runFetch(reqCtx context.Context, s *slot, url string, settleOverride time.Duration) (html, finalURL string, err error) {
	// Reuse the slot's tab rather than NewContext-ing a fresh one
	// per call. Containerised Chromium hangs CaptureScreenshot when
	// it's the first action in a brand-new tab; reusing the tab
	// avoids that race because subsequent Navigates put us back at
	// the same page-lifecycle stage. Anonymous-only daemon, so
	// per-tab isolation isn't a feature we need.
	timeout := d.Options.Timeout
	if timeout == 0 {
		timeout = headless.DefaultOptions.Timeout
	}
	timedCtx, cancelTimeout := context.WithTimeout(s.browserCtx, timeout)
	defer cancelTimeout()

	actions := []chromedp.Action{
		network.Enable(),
		chromedp.ActionFunc(func(ctx context.Context) error {
			_, e := page.AddScriptToEvaluateOnNewDocument(stealthScript).Do(ctx)
			return e
		}),
		chromedp.Navigate(url),
		chromedp.WaitReady("body", chromedp.ByQuery),
	}
	settle := settleOverride
	if settle == 0 {
		settle = d.Options.Settle
	}
	if settle == 0 {
		settle = headless.DefaultOptions.Settle
	}
	if settle > 0 {
		actions = append(actions, chromedp.Sleep(settle))
	}
	actions = append(actions,
		chromedp.OuterHTML("html", &html, chromedp.ByQuery),
		chromedp.Location(&finalURL),
	)

	// Suppress "context cancelled" noise: when r.Context() fires
	// (client disconnected), the timed context inherits and the
	// chromedp call returns "context cancelled". Surface it as a
	// 502 with a clear message.
	_ = reqCtx
	if err = chromedp.Run(timedCtx, actions...); err != nil {
		return "", "", err
	}
	if finalURL == "" {
		finalURL = url
	}
	return html, finalURL, nil
}

// statusResponse is the GET /status JSON. Summary counters plus
// per-slot detail and a recent-fetch ring — enough for the monitor
// to render a live view of "what's the daemon doing right now?"
// without needing a separate /events stream.
//
// UsesRemaining is kept for backwards compat with v1 clients; new
// callers should read Slots[].UsesRemaining instead.
type statusResponse struct {
	PoolSize      int          `json:"pool_size"`
	InUse         int          `json:"in_use"`
	RecycleAfter  int          `json:"recycle_after"`
	UpSeconds     int64        `json:"up_seconds"`
	Slots         []slotState  `json:"slots"`
	History       []fetchEntry `json:"history"`
	UsesRemaining []int        `json:"uses_remaining"` // deprecated
}

func (d *Daemon) handleStatus(w http.ResponseWriter, _ *http.Request) {
	d.stateMu.Lock()
	slots := make([]slotState, 0, d.PoolSize)
	uses := make([]int, 0, d.PoolSize)
	inUse := 0
	for i := 0; i < d.PoolSize; i++ {
		s, ok := d.state.slots[i]
		if !ok {
			continue
		}
		slots = append(slots, *s)
		uses = append(uses, s.UsesRemaining)
		if !s.Free {
			inUse++
		}
	}
	history := d.state.history.snapshot()
	d.stateMu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(statusResponse{
		PoolSize:      d.PoolSize,
		InUse:         inUse,
		RecycleAfter:  d.RecycleAfter,
		UpSeconds:     int64(time.Since(d.startAt).Seconds()),
		Slots:         slots,
		History:       history,
		UsesRemaining: uses, // deprecated mirror
	})
}

// handleHealth is the liveness-probe twin to /status — sub-millisecond
// 200 OK with `ok\n`, no JSON parsing required. Used by container
// HEALTHCHECK directives so Docker can decide quickly whether the
// pool is up.
func (d *Daemon) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, "ok\n")
}

// handleMonitor renders the daemon's status as a human-readable
// text page. Snapshot equivalent of the older `social-fetch headless
// monitor`, suitable for `curl … | less` operator checks inside
// containers where parsing JSON /status by hand is awkward.
func (d *Daemon) handleMonitor(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")

	d.stateMu.Lock()
	slots := make([]slotState, 0, d.PoolSize)
	uses := make([]int, 0, d.PoolSize)
	inUse := 0
	for i := 0; i < d.PoolSize; i++ {
		s, ok := d.state.slots[i]
		if !ok {
			continue
		}
		slots = append(slots, *s)
		uses = append(uses, s.UsesRemaining)
		if !s.Free {
			inUse++
		}
	}
	hist := d.state.history.snapshot()
	d.stateMu.Unlock()

	up := time.Since(d.startAt).Round(time.Second)
	fmt.Fprintln(w, "social-browser local pool")
	fmt.Fprintf(w, "  uptime         %s\n", up)
	fmt.Fprintf(w, "  pool size      %d\n", d.PoolSize)
	fmt.Fprintf(w, "  in use         %d\n", inUse)
	fmt.Fprintf(w, "  recycle after  %d\n", d.RecycleAfter)

	fmt.Fprintln(w, "\nslots:")
	for _, s := range slots {
		state := "free"
		if !s.Free {
			state = fmt.Sprintf("busy %s", s.CurrentURL)
		}
		fmt.Fprintf(w, "  [%d] uses=%d total=%d  %s\n",
			s.ID, s.UsesRemaining, s.TotalFetches, state)
	}

	if len(hist) == 0 {
		fmt.Fprintln(w, "\nrecent fetches: (none)")
		return
	}
	fmt.Fprintln(w, "\nrecent fetches (newest first):")
	for _, e := range hist {
		ok := "ok"
		if !e.OK {
			ok = "FAIL"
		}
		fmt.Fprintf(w, "  %s  slot=%d  %dms  %s  %s\n",
			e.StartAt.Format("15:04:05"), e.SlotID, e.DurMS, ok, e.URL)
	}
}

// handleShutdown is opt-in graceful shutdown for tests / scripts.
// Production shutdowns go through the OS signal that runs Run()'s
// context cancellation. We close pool slots and let Run return.
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

// screenshotRequest mirrors fetchRequest: URL in via JSON body. Settle
// + full_page come in as query params so the same handler shape works
// for both endpoints.
type screenshotRequest struct {
	URL string `json:"url"`
}

// handleScreenshot is the /screenshot HTTP handler. Acquires a slot
// from the pool, navigates, captures via page.CaptureScreenshot
// (captureBeyondViewport=true for full-page) or
// chromedp.CaptureScreenshot (viewport-only), and returns the PNG bytes with
// Content-Type image/png. Same slot lifecycle as handleFetch — busy
// markers, recycle on error, history ring updated.
//
// Query params: ?full_page=1 (default true) toggles full-page vs
// viewport-only. ?settle=2s overrides the daemon's default settle
// for this call.
func (d *Daemon) handleScreenshot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 4096))
	if err != nil {
		http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
		return
	}
	var req screenshotRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "decode body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.URL) == "" {
		http.Error(w, "url is required", http.StatusBadRequest)
		return
	}

	fullPage := true
	if v := strings.TrimSpace(r.URL.Query().Get("full_page")); v != "" {
		switch strings.ToLower(v) {
		case "0", "false", "no", "off":
			fullPage = false
		}
	}
	settleOverride := time.Duration(0)
	if v := strings.TrimSpace(r.URL.Query().Get("settle")); v != "" {
		if d2, err := time.ParseDuration(v); err == nil && d2 > 0 {
			settleOverride = d2
		}
	}

	var s *slot
	select {
	case s = <-d.pool:
	case <-r.Context().Done():
		http.Error(w, "client cancelled", http.StatusRequestTimeout)
		return
	}

	startAt := time.Now()
	d.markBusy(s.id, req.URL, startAt)

	png, finalURL, fetchErr := d.runScreenshot(r.Context(), s, req.URL, settleOverride, fullPage)
	dur := time.Since(startAt)

	released := d.releaseSlot(s, fetchErr != nil)
	d.markFree(released, req.URL, startAt, dur, fetchErr)
	d.pool <- released

	if fetchErr != nil {
		d.Logf("slot %d: screenshot %s failed: %v", s.id, req.URL, fetchErr)
		http.Error(w, fetchErr.Error(), http.StatusBadGateway)
		return
	}
	writeScreenshotResponse(w, png, finalURL)
}

// writeScreenshotResponse serialises a PNG to w with Content-Length
// set up front so net/http does NOT fall back to Transfer-Encoding:
// chunked.
//
// Daytona's L7 proxy buffers chunked binary responses badly — a
// 20 KB PNG that streams out in 2s from chromedp gets held by the
// proxy until its 60s deadline, surfacing as a 502 "context deadline
// exceeded" on the client side. Setting Content-Length up front
// gives the proxy a well-shaped HTTP/1.1 response it can forward
// without buffering. Extracted from handleScreenshot so the
// chunked-vs-fixed-length framing contract is testable without
// spinning up real chromedp.
func writeScreenshotResponse(w http.ResponseWriter, png []byte, finalURL string) {
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("X-Final-URL", finalURL)
	w.Header().Set("Content-Length", strconv.Itoa(len(png)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(png)
}

// runScreenshot captures a PNG inside an existing warm browser slot.
// Same shape as runFetch — reuses the slot's tab (see runFetch
// comment for why we don't NewContext per call), navigate, settle,
// capture, return.
func (d *Daemon) runScreenshot(reqCtx context.Context, s *slot, url string, settleOverride time.Duration, fullPage bool) (png []byte, finalURL string, err error) {
	timeout := d.Options.Timeout
	if timeout == 0 {
		timeout = headless.DefaultOptions.Timeout
	}
	timedCtx, cancelTimeout := context.WithTimeout(s.browserCtx, timeout)
	defer cancelTimeout()

	actions := []chromedp.Action{
		network.Enable(),
		chromedp.ActionFunc(func(ctx context.Context) error {
			_, e := page.AddScriptToEvaluateOnNewDocument(stealthScript).Do(ctx)
			return e
		}),
		chromedp.Navigate(url),
		chromedp.WaitReady("body", chromedp.ByQuery),
	}
	settle := settleOverride
	if settle == 0 {
		settle = d.Options.Settle
	}
	if settle == 0 {
		settle = headless.DefaultOptions.Settle
	}
	if settle > 0 {
		actions = append(actions, chromedp.Sleep(settle))
	}
	if fullPage {
		// Use page.CaptureScreenshot with captureBeyondViewport=true
		// instead of chromedp.FullScreenshot. The latter is a higher-
		// level helper that calls emulation.SetDeviceMetricsOverride
		// to resize the layout viewport before capture; that path
		// hangs indefinitely in the older Chromium shipped by
		// debian-bookworm-slim (the runtime image base) while
		// CaptureScreenshot variants return in ~2s.
		// captureBeyondViewport gives us "whole scrollable page"
		// semantics without the layout-recompute step.
		actions = append(actions, chromedp.ActionFunc(func(ctx context.Context) error {
			b, err := page.CaptureScreenshot().
				WithFormat(page.CaptureScreenshotFormatPng).
				WithCaptureBeyondViewport(true).
				WithFromSurface(true).
				Do(ctx)
			if err != nil {
				return err
			}
			png = b
			return nil
		}))
	} else {
		actions = append(actions, chromedp.CaptureScreenshot(&png))
	}
	actions = append(actions, chromedp.Location(&finalURL))

	_ = reqCtx
	if err = chromedp.Run(timedCtx, actions...); err != nil {
		return nil, "", err
	}
	if finalURL == "" {
		finalURL = url
	}
	return png, finalURL, nil
}

// shutdown drains the pool and tears down every slot's browser.
// Idempotent — multiple calls are safe.
func (d *Daemon) shutdown() {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return
	}
	d.closed = true
	d.mu.Unlock()

	if d.pool == nil {
		return
	}
	close(d.pool)
	for s := range d.pool {
		s.cancelBrowser()
		s.cancelAlloc()
	}
}
