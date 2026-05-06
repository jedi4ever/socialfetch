package browser

// Local browser-pool daemon — exposes the same HTTP surface as
// the per-sandbox chromedp daemon (POST /fetch, POST /screenshot,
// GET /status, GET /monitor, GET /health) but forwards every
// request to a backend in the Fleet, attaching the per-backend
// auth token transparently.
//
// Lifecycle: NewDaemon → SetProvider → Run(ctx, addr). Run blocks
// until ctx cancels or ListenAndServe errors. A background
// health-check goroutine refreshes Fleet liveness every 30s.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// DefaultDaemonPort is what `social-browser daemon start` binds
// when no --bind is given. Picked next-in-sequence after 5556
// (per-sandbox chromedp), 5557 (ledger), 5558 (MCP), 5559
// (reserved). Local clients (social-fetch) point
// SOCIAL_FETCH_HEADLESS_DAEMON_URL at http://127.0.0.1:5560 and
// the daemon hides the fleet entirely.
const DefaultDaemonPort = 5560

// Daemon is the long-lived HTTP server that fronts a Fleet of
// browser backends. Cheap to construct; the real work happens in
// Run.
type Daemon struct {
	// Fleet is the live backend list. SetProvider seeds it.
	Fleet *Fleet

	// Provider is the substrate that creates / refreshes
	// backends. Required before Run.
	Provider Provider

	// HealthInterval is how often the background loop probes
	// each backend's /status. 0 = default 30s. Lower values
	// catch dead backends faster at the cost of more network
	// chatter.
	HealthInterval time.Duration

	// Logf is the audit hook (one line per significant event:
	// fetch forwarded, backend marked dead, token refreshed).
	// Nil = no-op. CLI sets this to fmt.Fprintf(os.Stderr,...).
	Logf func(format string, a ...any)

	// OnlyID, when non-empty, restricts the fleet to a single
	// backend ID after Provider.List(). Useful for debugging — pin
	// every forwarded request to one specific sandbox instead of
	// round-robining, so its log + behaviour can be traced in
	// isolation.
	OnlyID string

	// Verbose dumps extra detail per forwarded request: outgoing
	// URL, request body length, response status, response body
	// snippet on non-2xx. Off by default — flip on with
	// `daemon run --verbose` when chasing a 404 / 502 from upstream.
	Verbose bool

	startAt time.Time
	mu      sync.Mutex
	closed  bool
}

// Run opens the server on addr, refreshes the fleet from the
// provider, starts the health-check goroutine, and blocks until
// ctx cancels.
func (d *Daemon) Run(ctx context.Context, addr string) error {
	if d.Provider == nil {
		return errors.New("browser daemon: Provider is required")
	}
	if d.Fleet == nil {
		d.Fleet = NewFleet()
	}
	if d.Logf == nil {
		d.Logf = func(string, ...any) {}
	}
	if d.HealthInterval <= 0 {
		d.HealthInterval = 30 * time.Second
	}
	d.startAt = time.Now()

	// Initial fleet discovery — Provider is the source of truth,
	// no local persistence. If the provider's network is down
	// at startup, log the error but keep the daemon alive; the
	// health-check loop will retry.
	if err := d.refreshFleet(ctx); err != nil {
		d.Logf("initial fleet refresh failed (%v) — continuing with empty fleet", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/fetch", d.handleProxy("/fetch"))
	mux.HandleFunc("/screenshot", d.handleProxy("/screenshot"))
	mux.HandleFunc("/status", d.handleStatus)
	mux.HandleFunc("/monitor", d.handleMonitor)
	mux.HandleFunc("/health", d.handleHealth)
	mux.HandleFunc("/shutdown", d.handleShutdown)

	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	// Health-check goroutine.
	healthCtx, healthCancel := context.WithCancel(ctx)
	defer healthCancel()
	go d.healthLoop(healthCtx)

	// Cancel listener on ctx done.
	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
	}()

	d.Logf("listening on %s, provider=%s, backends=%d", addr, d.Provider.Name(), len(d.Fleet.All()))
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// refreshFleet pulls the current backend list from Provider and
// swaps it into the Fleet. Marks every entry "ready" optimistically;
// the health-check loop downgrades dead ones. When OnlyID is set,
// filters to that one backend so debugging sessions can pin all
// traffic to one sandbox.
func (d *Daemon) refreshFleet(ctx context.Context) error {
	bs, err := d.Provider.List(ctx)
	if err != nil {
		return err
	}
	if d.OnlyID != "" {
		filtered := bs[:0]
		for _, b := range bs {
			if b.ID == d.OnlyID {
				filtered = append(filtered, b)
			}
		}
		if len(filtered) == 0 {
			return fmt.Errorf("backend id %q not found in provider's fleet", d.OnlyID)
		}
		bs = filtered
	}
	for i := range bs {
		if bs[i].State == "" {
			bs[i].State = "ready"
		}
	}
	d.Fleet.Replace(bs)
	return nil
}

// handleProxy returns an http.HandlerFunc that forwards the
// incoming POST body verbatim to the chosen backend's `path`,
// attaching auth headers and the original query string. Streams
// the response body straight back so big payloads (full-page
// screenshots) don't get buffered.
func (d *Daemon) handleProxy(path string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		be, release, err := d.Fleet.Pick()
		if err != nil {
			http.Error(w, "no ready backends in fleet — run `social-browser provider <name> up -n N`", http.StatusServiceUnavailable)
			return
		}
		defer release()

		// Read body so we can retry on 401 (token-refresh path).
		body, err := io.ReadAll(io.LimitReader(r.Body, 16*1024*1024))
		if err != nil {
			http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
			return
		}

		ctype := r.Header.Get("Content-Type")
		if ctype == "" {
			// Daytona's chromedp daemon checks Content-Type on /fetch
			// and returns 404 when it's empty. social-fetch sets it on
			// every call; this guards against incoming probes that
			// forgot, so the operator gets a useful error rather than
			// a confusing 404 from the upstream.
			ctype = "application/json"
		}
		out, status, err := d.forward(r.Context(), be, path, r.URL.RawQuery, body, ctype)
		if status == http.StatusUnauthorized {
			// URL/token expired — refresh and retry once. Daytona
			// signed URLs rotate per call (fresh short-id hostname
			// + fresh embedded auth), so refresh swaps the whole
			// Backend, not just a header token.
			d.Logf("backend %s 401 on %s, refreshing url+token", be.ID, path)
			if fresh, terr := d.Provider.RefreshBackend(r.Context(), be.ID); terr == nil {
				d.Fleet.UpdateBackend(be.ID, fresh)
				be.URL = fresh.URL
				be.Token = fresh.Token
				out, status, err = d.forward(r.Context(), be, path, r.URL.RawQuery, body, ctype)
			}
		} else if isProxyColdStart(status, out) {
			// Daytona's proxy returns 404 "Not found." on the first
			// request to a sandbox port that hasn't yet seen traffic
			// (the upstream socket isn't plumbed through). A short
			// pause + retry usually clears it. Limit to one extra
			// attempt so a genuine 404 from chromedp ("unknown URL")
			// still surfaces quickly.
			d.Logf("backend %s cold-start 404 on %s, retrying once", be.ID, path)
			time.Sleep(750 * time.Millisecond)
			out, status, err = d.forward(r.Context(), be, path, r.URL.RawQuery, body, ctype)
		}
		if err != nil {
			d.Fleet.MarkDead(be.ID)
			d.Logf("backend %s %s FAILED: %v", be.ID, path, err)
			http.Error(w, "backend "+be.ID+": "+err.Error(), http.StatusBadGateway)
			return
		}
		// On non-2xx, also log the first chunk of the body — turns a
		// silent "status=404 (10 bytes)" into actionable signal
		// ("404: Not Found" / "401: token expired" / etc).
		if status < 200 || status >= 300 {
			snippet := strings.TrimSpace(string(out.Body))
			if len(snippet) > 200 {
				snippet = snippet[:200] + "…"
			}
			d.Logf("forwarded %s → %s status=%d body=%q", path, be.ID, status, snippet)
		} else {
			d.Logf("forwarded %s → %s status=%d (%d bytes)", path, be.ID, status, len(out.Body))
		}
		for k, v := range out.Header {
			for _, vv := range v {
				w.Header().Add(k, vv)
			}
		}
		w.WriteHeader(status)
		_, _ = w.Write(out.Body)
	}
}

// proxyResponse is what forward returns. Keeps body + headers
// together so the caller can replay them onto the client
// ResponseWriter.
type proxyResponse struct {
	Body   []byte
	Header http.Header
}

// forward is the per-request HTTP call: build URL, attach auth,
// POST body, return response bytes + status. Returns status alone
// (not wrapped in error) for 4xx/5xx so the caller can decide
// whether to retry on 401.
func (d *Daemon) forward(ctx context.Context, be *Backend, path, query string, body []byte, contentType string) (*proxyResponse, int, error) {
	url := strings.TrimRight(be.URL, "/") + path
	if query != "" {
		url += "?" + query
	}
	if d.Verbose {
		d.Logf("→ %s %s (body=%d bytes, ct=%q, token=%s)",
			http.MethodPost, url, len(body), contentType, tokenPrefix(be.Token))
	}
	reqCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if be.Token != "" {
		req.Header.Set("Authorization", "Bearer "+be.Token)
		req.Header.Set("X-Daytona-Preview-Token", be.Token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return &proxyResponse{Body: out, Header: resp.Header.Clone()}, resp.StatusCode, nil
}

// tokenPrefix returns the first 8 chars of a token, or "<empty>".
// Used in --verbose logs to confirm auth is attached without
// dumping the full token to disk.
func tokenPrefix(token string) string {
	if token == "" {
		return "<empty>"
	}
	if len(token) <= 8 {
		return token + "…"
	}
	return token[:8] + "…"
}

// isProxyColdStart matches the specific shape of Daytona's
// proxy-not-warm response: HTTP 404 with body literally
// "Not found." (10 bytes). Kept narrow on purpose — a genuine
// chromedp 404 ("path /foo not registered") is a different
// failure mode and shouldn't be retried.
func isProxyColdStart(status int, resp *proxyResponse) bool {
	if status != http.StatusNotFound || resp == nil {
		return false
	}
	return strings.TrimSpace(string(resp.Body)) == "Not found."
}

// healthLoop probes each backend's /status periodically. Alive
// = MarkAlive (resets dead counter), error = MarkDead (counter
// climbs; >3 consecutive flips State to "dead"). Also catches
// new backends added via the Provider since startup — the loop
// re-pulls Provider.List every cycle so `up` from a separate
// `social-browser provider daytona up` invocation is picked up
// without restarting the daemon.
func (d *Daemon) healthLoop(ctx context.Context) {
	ticker := time.NewTicker(d.HealthInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		// Re-pull from provider in case the operator added
		// backends. This is a best-effort refresh — a network
		// blip just means the existing fleet keeps serving until
		// the next cycle.
		if err := d.refreshFleet(ctx); err != nil {
			d.Logf("fleet refresh failed: %v", err)
		}
		for _, be := range d.Fleet.All() {
			if d.probeAlive(ctx, &be) {
				d.Fleet.MarkAlive(be.ID)
			} else {
				d.Fleet.MarkDead(be.ID)
			}
		}
	}
}

// probeAlive does a short GET /status against one backend. 200 =
// alive. Errors / non-2xx = dead for this cycle.
func (d *Daemon) probeAlive(ctx context.Context, be *Backend) bool {
	probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, strings.TrimRight(be.URL, "/")+"/status", nil)
	if err != nil {
		return false
	}
	if be.Token != "" {
		req.Header.Set("Authorization", "Bearer "+be.Token)
		req.Header.Set("X-Daytona-Preview-Token", be.Token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode >= 200 && resp.StatusCode < 300
}

// ----- read-side endpoints -----

func (d *Daemon) handleStatus(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	bs := d.Fleet.All()
	type backendOut struct {
		ID       string `json:"id"`
		Provider string `json:"provider"`
		URL      string `json:"url"`
		State    string `json:"state"`
		InFlight string `json:"in_flight"`
		Dead     string `json:"dead_count"`
	}
	out := make([]backendOut, 0, len(bs))
	for _, b := range bs {
		out = append(out, backendOut{
			ID:       b.ID,
			Provider: b.Provider,
			URL:      b.URL,
			State:    b.State,
			InFlight: b.Labels["__inflight"],
			Dead:     b.Labels["__dead_count"],
		})
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"up_seconds":    int64(time.Since(d.startAt).Seconds()),
		"provider":      d.Provider.Name(),
		"backend_count": len(bs),
		"backends":      out,
	})
}

func (d *Daemon) handleMonitor(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	bs := d.Fleet.All()
	fmt.Fprintf(w, "social-browser daemon — provider=%s\n", d.Provider.Name())
	fmt.Fprintf(w, "  uptime         %s\n", time.Since(d.startAt).Round(time.Second))
	fmt.Fprintf(w, "  backend count  %d\n\n", len(bs))
	if len(bs) == 0 {
		fmt.Fprintln(w, "(no backends — try `social-browser provider <name> up -n N`)")
		return
	}
	fmt.Fprintln(w, "backends:")
	for _, b := range bs {
		fmt.Fprintf(w, "  %s  state=%-7s in_flight=%-3s dead=%-2s  %s\n",
			b.ID, b.State,
			b.Labels["__inflight"], b.Labels["__dead_count"],
			b.URL)
	}
}

// handleHealth: 200 if at least one backend is "ready", 503
// otherwise. Used by container HEALTHCHECK directives /
// uptime-monitoring scripts.
func (d *Daemon) handleHealth(w http.ResponseWriter, _ *http.Request) {
	for _, b := range d.Fleet.All() {
		if b.State == "ready" {
			w.Header().Set("Content-Type", "text/plain")
			_, _ = io.WriteString(w, "ok\n")
			return
		}
	}
	http.Error(w, "no ready backends", http.StatusServiceUnavailable)
}

// handleShutdown is opt-in graceful shutdown for tests / scripts.
func (d *Daemon) handleShutdown(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	w.WriteHeader(http.StatusAccepted)
	go func() {
		time.Sleep(100 * time.Millisecond)
		d.mu.Lock()
		d.closed = true
		d.mu.Unlock()
	}()
}
