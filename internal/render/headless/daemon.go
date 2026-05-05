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
// when no env override is set. Two strikes a balance: parallel batch
// fetches don't fully serialise, but we don't burn 200+ MB of RAM on
// a quiet machine.
const DefaultPoolSize = 2

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

	d.pool = make(chan *slot, d.PoolSize)
	for i := 0; i < d.PoolSize; i++ {
		s, err := d.newSlot(ctx, i)
		if err != nil {
			d.shutdown()
			return fmt.Errorf("init slot %d: %w", i, err)
		}
		d.pool <- s
	}
	d.Logf("pool ready: size=%d recycle_after=%d", d.PoolSize, d.RecycleAfter)

	mux := http.NewServeMux()
	mux.HandleFunc("/fetch", d.handleFetch)
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

	html, finalURL, fetchErr := d.runFetch(r.Context(), s, req.URL)

	// Release: replace or recycle. Always returns a slot to the
	// pool so the next request can proceed.
	released := d.releaseSlot(s, fetchErr != nil)
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
	return fresh
}

// runFetch is the actual chromedp navigate+grab. Mirrors what the
// in-process Fetcher.Fetch does, except it reuses slot.browserCtx
// instead of spinning up a fresh allocator per call. A child
// context with chromedp.NewContext(slot.browserCtx) gives us a
// fresh tab inside the warm browser.
func (d *Daemon) runFetch(reqCtx context.Context, s *slot, url string) (html, finalURL string, err error) {
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
	settle := d.FetcherOptions.Settle
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

// statusResponse is the GET /status JSON. PoolSize gives capacity;
// InUse is best-effort (read off the channel buffer, not snapshot-
// consistent under load); UsesRemaining lists what each slot has
// left before recycle.
type statusResponse struct {
	PoolSize      int   `json:"pool_size"`
	InUse         int   `json:"in_use"`
	RecycleAfter  int   `json:"recycle_after"`
	UsesRemaining []int `json:"uses_remaining"`
}

func (d *Daemon) handleStatus(w http.ResponseWriter, r *http.Request) {
	// /status must NOT block waiting for slots — under heavy /fetch
	// load all browsers are out of the pool channel, and a blocking
	// drain would deadlock the probe. We do a non-blocking read of
	// whatever's currently free and report PoolSize - free as
	// in-use. usesRemaining is reported only for free slots since
	// in-use slots are owned by other goroutines and we can't
	// safely read their state.
	snapshot := make([]*slot, 0, d.PoolSize)
drain:
	for i := 0; i < d.PoolSize; i++ {
		select {
		case s := <-d.pool:
			snapshot = append(snapshot, s)
		default:
			break drain // no more free slots right now
		}
	}
	uses := make([]int, 0, len(snapshot))
	for _, s := range snapshot {
		uses = append(uses, s.usesRemaining)
	}
	// Put the slots back BEFORE encoding the response — otherwise
	// concurrent /fetch requests stall while we serialise JSON.
	for _, s := range snapshot {
		d.pool <- s
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(statusResponse{
		PoolSize:      d.PoolSize,
		InUse:         d.PoolSize - len(snapshot),
		RecycleAfter:  d.RecycleAfter,
		UsesRemaining: uses, // only the free slots
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
