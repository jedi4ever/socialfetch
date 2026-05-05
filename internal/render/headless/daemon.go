package headless

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

// DefaultDaemonPort is the loopback port `social-fetch headless start`
// listens on. Picked to sit next to the bridge's :5555 so operators
// keep one mental model for "social-fetch's local services live in
// the 5555-5559 range." Override with --bind on `headless start` or
// SOCIAL_FETCH_HEADLESS_DAEMON_URL on clients.
const DefaultDaemonPort = 5556

// DefaultPoolSize is how many warm browsers the daemon keeps ready
// when no env override is set. Four matches `social-fetch fetch -j`'s
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

// Daemon is the long-lived headless-browser pool exposed over HTTP.
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

	// FetcherOptions overrides what each browser is launched with.
	// Empty fields fall back to DefaultOptions equivalents (same as
	// in-process NewWithOptions). Cookies are *not* honoured on the
	// daemon today — daemon mode is anonymous-only.
	FetcherOptions Options

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
// enough to populate a `headless monitor` view; older entries are
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
	opts := d.FetcherOptions
	// Fill defaults same way NewWithOptions does so the daemon
	// browsers match in-process behaviour exactly.
	if opts.UserAgent == "" {
		opts.UserAgent = DefaultOptions.UserAgent
	}
	if opts.Locale == "" {
		opts.Locale = DefaultOptions.Locale
	}
	if opts.Timezone == "" {
		opts.Timezone = DefaultOptions.Timezone
	}
	if opts.ViewportWidth == 0 {
		opts.ViewportWidth = DefaultOptions.ViewportWidth
	}
	if opts.ViewportHeight == 0 {
		opts.ViewportHeight = DefaultOptions.ViewportHeight
	}
	if opts.Settle == 0 {
		opts.Settle = DefaultOptions.Settle
	}
	opts.Headless = true // daemon never wants a visible window

	allocOpts := buildAllocatorOpts(opts)
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(parent, allocOpts...)
	browserCtx, cancelBrowser := chromedp.NewContext(allocCtx)

	// Force Chrome launch by running a no-op — without this the
	// real launch happens lazily on the first chromedp.Run, which
	// would mean the first /fetch eats the warmup cost we want to
	// pay here at startup.
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

// runFetch is the actual chromedp navigate+grab. Mirrors what the
// in-process Fetcher.Fetch does, except it reuses slot.browserCtx
// instead of spinning up a fresh allocator per call. A child
// context with chromedp.NewContext(slot.browserCtx) gives us a
// fresh tab inside the warm browser.
//
// settleOverride > 0 replaces the daemon's default settle for
// this call. Used by callers retrying a thin-content fetch
// (article fetcher does this automatically when the first
// attempt's body is suspiciously short — likely a JS-rendered
// SPA that needed more hydration time).
func (d *Daemon) runFetch(reqCtx context.Context, s *slot, url string, settleOverride time.Duration) (html, finalURL string, err error) {
	// New tab inside the warm browser. cancel() closes the tab,
	// not the browser.
	tabCtx, cancelTab := chromedp.NewContext(s.browserCtx)
	defer cancelTab()

	timeout := d.FetcherOptions.Timeout
	if timeout == 0 {
		timeout = DefaultOptions.Timeout
	}
	timedCtx, cancelTimeout := context.WithTimeout(tabCtx, timeout)
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
		settle = d.FetcherOptions.Settle
	}
	if settle == 0 {
		settle = DefaultOptions.Settle
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
// per-slot detail and a recent-fetch ring — enough for `headless
// monitor` to render a live view of "what's the daemon doing right
// now?" without needing a separate /events stream.
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

func (d *Daemon) handleStatus(w http.ResponseWriter, r *http.Request) {
	// All state lives in d.state (mutex-guarded) — no need to
	// touch the pool channel for status, which means /status
	// answers instantly even under heavy /fetch load.
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

// handleShutdown is opt-in graceful shutdown for tests / scripts.
// Production shutdowns go through the OS signal that runs Run()'s
// context cancellation. We close pool slots and let Run return.
func (d *Daemon) handleShutdown(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	w.WriteHeader(http.StatusAccepted)
	// Async shutdown so the response can flush before the server stops.
	go func() {
		time.Sleep(100 * time.Millisecond)
		d.shutdown()
	}()
}

// screenshotRequest mirrors fetchRequest: URL in via JSON body. Settle
// + full_page come in as query params so the same handler shape works
// for both endpoints. Could be in the body — kept on the URL for
// parity with `?settle=` already used on /fetch.
type screenshotRequest struct {
	URL string `json:"url"`
}

// handleScreenshot is the /screenshot HTTP handler. Acquires a slot
// from the pool, navigates, captures via chromedp.FullScreenshot or
// chromedp.CaptureScreenshot, and returns the PNG bytes with
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
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("X-Final-URL", finalURL)
	_, _ = w.Write(png)
}

// runScreenshot captures a PNG inside an existing warm browser slot.
// Same shape as runFetch — fresh tab via chromedp.NewContext on the
// slot's browser context, navigate, settle, capture, return.
func (d *Daemon) runScreenshot(reqCtx context.Context, s *slot, url string, settleOverride time.Duration, fullPage bool) (png []byte, finalURL string, err error) {
	tabCtx, cancelTab := chromedp.NewContext(s.browserCtx)
	defer cancelTab()

	timeout := d.FetcherOptions.Timeout
	if timeout == 0 {
		timeout = DefaultOptions.Timeout
	}
	timedCtx, cancelTimeout := context.WithTimeout(tabCtx, timeout)
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
		settle = d.FetcherOptions.Settle
	}
	if settle == 0 {
		settle = DefaultOptions.Settle
	}
	if settle > 0 {
		actions = append(actions, chromedp.Sleep(settle))
	}
	if fullPage {
		actions = append(actions, chromedp.FullScreenshot(&png, 100))
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
	// Drain the pool best-effort. Anything still in flight will
	// hit a closed context when it tries to release.
	close(d.pool)
	for s := range d.pool {
		s.cancelBrowser()
		s.cancelAlloc()
	}
}
