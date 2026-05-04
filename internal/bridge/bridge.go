// Package bridge connects a Chrome extension (running in the user's
// logged-in browser) to social-fetch fetchers that need an authenticated
// page render — LinkedIn posts, X for-you feed, anything that requires a
// real session.
//
// Architecture:
//
//	[browser extension] ←ws→ /ws/extension ─┐
//	                                        │
//	                              [bridge server]
//	                                        │
//	[fetcher / CLI client] ──http POST──→ /cmd
//
// The extension opens a WebSocket and waits for commands. Fetchers POST
// JSON to /cmd; the bridge tags each request with a unique id, forwards
// it over the WS, and waits up to a deadline for a response with the
// matching id, which it relays back as the HTTP response body.
//
// The model assumes a single connected extension (matches the user's
// browser-extension setup); a second connection bumps the first.
package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

// DefaultPort is the WebSocket port the bundled extension expects.
const DefaultPort = 5555

// DefaultCommandTimeout caps how long /cmd waits for the extension to
// respond before returning 504. LinkedIn posts can take 30-60s to
// render after navigation when the page is heavy (lots of comments,
// reactions, reposts) — the default leaves headroom for that. Bump
// via SOCIAL_BRIDGE_TIMEOUT (Server.CommandTimeout reads it on
// startup) when you're hitting timeouts on a slow network.
const DefaultCommandTimeout = 90 * time.Second

// Server is a single-extension bridge. Construct with New, then call
// Routes() to mount on an http.ServeMux, or Run() to serve directly.
type Server struct {
	// Logf is called for human-readable diagnostics. Defaults to no-op.
	Logf func(format string, args ...any)

	// CommandTimeout overrides DefaultCommandTimeout when non-zero.
	CommandTimeout time.Duration

	mu      sync.Mutex
	conn    *websocket.Conn // current extension connection (or nil)
	pending map[string]chan json.RawMessage
	nextID  atomic.Uint64
}

// New returns a Server with no logger and the SOCIAL_BRIDGE_TIMEOUT-
// configured command timeout (or DefaultCommandTimeout when unset).
// Same env var the client honours, so daemon + client scale together
// when an operator bumps it for slow networks.
func New() *Server {
	return &Server{
		pending:        map[string]chan json.RawMessage{},
		CommandTimeout: bridgeTimeout(),
		Logf:           func(string, ...any) {},
	}
}

// Routes registers the bridge HTTP endpoints onto a mux. The two routes
// are intentionally narrow:
//
//	GET  /ws/extension   WebSocket upgrade for the browser extension
//	POST /cmd            JSON command from a fetcher
func (s *Server) Routes(mux *http.ServeMux) {
	mux.HandleFunc("/ws/extension", s.handleWS)
	mux.HandleFunc("/cmd", s.handleCmd)
	mux.HandleFunc("/status", s.handleStatus)
}

// handleStatus returns the bridge's connection state. Always 200 when
// the bridge itself is reachable; the body's `connected` boolean tells
// the caller whether an extension is actually attached.
func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	body, _ := json.Marshal(map[string]any{
		"connected": s.Connected(),
	})
	_, _ = w.Write(body)
}

// Run starts an HTTP server on addr and serves the bridge. Blocks until
// the context is cancelled or the server errors out.
func (s *Server) Run(ctx context.Context, addr string) error {
	mux := http.NewServeMux()
	s.Routes(mux)
	srv := &http.Server{Addr: addr, Handler: mux}

	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	s.Logf("bridge listening on %s (ws://127.0.0.1%s/ws/extension)", addr, addr)

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

// Connected reports whether an extension is currently attached.
func (s *Server) Connected() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.conn != nil
}

// ----- WebSocket side (extension client) ---------------------------------

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		// Chrome MV3 extensions on a 127.0.0.1 origin: skip origin check.
		InsecureSkipVerify: true,
	})
	if err != nil {
		s.Logf("ws accept failed: %v", err)
		return
	}
	// Bump any existing connection — a second extension session replaces
	// the first. This matches the user's setup where there's only ever
	// one logged-in browser running the extension.
	s.swapConn(c)
	defer s.dropConn(c)

	s.Logf("extension connected from %s", r.RemoteAddr)
	c.SetReadLimit(8 * 1024 * 1024) // posts can be hundreds of KB of HTML

	for {
		_, data, err := c.Read(r.Context())
		if err != nil {
			s.Logf("extension read closed: %v", err)
			return
		}
		// Two flavors of incoming message:
		//   1. {type: "hello"} or {type: "settings"} — informational
		//   2. {id, command, status, ...result} — reply to a /cmd
		var probe struct {
			Type string `json:"type"`
			ID   any    `json:"id"`
		}
		if err := json.Unmarshal(data, &probe); err != nil {
			s.Logf("extension sent bad json: %v", err)
			continue
		}
		if probe.ID != nil {
			s.deliver(probe.ID, data)
			continue
		}
		// Informational — log and move on.
		s.Logf("extension said: %s", string(data))
	}
}

func (s *Server) swapConn(c *websocket.Conn) {
	s.mu.Lock()
	old := s.conn
	s.conn = c
	s.mu.Unlock()
	if old != nil {
		_ = old.Close(websocket.StatusNormalClosure, "replaced by new connection")
	}
}

func (s *Server) dropConn(c *websocket.Conn) {
	s.mu.Lock()
	if s.conn == c {
		s.conn = nil
	}
	s.mu.Unlock()
}

// deliver routes a reply to the goroutine that issued the command.
func (s *Server) deliver(rawID any, data json.RawMessage) {
	id := stringifyID(rawID)
	s.mu.Lock()
	ch, ok := s.pending[id]
	if ok {
		delete(s.pending, id)
	}
	s.mu.Unlock()
	if !ok {
		s.Logf("dropping reply for unknown id %s", id)
		return
	}
	// Non-blocking send: receiver may have already given up.
	select {
	case ch <- data:
	default:
	}
}

func stringifyID(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	default:
		return fmt.Sprint(x)
	}
}

// ----- HTTP side (fetcher client) ----------------------------------------

func (s *Server) handleCmd(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	defer r.Body.Close()

	var payload map[string]any
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
		return
	}
	if _, ok := payload["command"]; !ok {
		http.Error(w, `missing "command"`, http.StatusBadRequest)
		return
	}

	timeout := s.CommandTimeout
	if timeout == 0 {
		timeout = DefaultCommandTimeout
	}

	reply, err := s.Send(r.Context(), payload, timeout)
	if err != nil {
		switch {
		case errors.Is(err, ErrNotConnected):
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
		case errors.Is(err, context.DeadlineExceeded):
			http.Error(w, err.Error(), http.StatusGatewayTimeout)
		default:
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(reply)
}

// ErrNotConnected is returned when no extension is currently attached.
var ErrNotConnected = errors.New("bridge: no extension connected")

// Send forwards a command to the extension and returns the matching
// reply. Used by /cmd, but also exposed so in-process callers (e.g.
// tests, an embedded fetcher) can talk to the bridge without HTTP.
func (s *Server) Send(ctx context.Context, payload map[string]any, timeout time.Duration) (json.RawMessage, error) {
	id := strconv.FormatUint(s.nextID.Add(1), 10)
	payload["id"] = id

	s.mu.Lock()
	conn := s.conn
	if conn == nil {
		s.mu.Unlock()
		return nil, ErrNotConnected
	}
	ch := make(chan json.RawMessage, 1)
	s.pending[id] = ch
	s.mu.Unlock()

	body, err := json.Marshal(payload)
	if err != nil {
		s.removePending(id)
		return nil, err
	}

	if err := conn.Write(ctx, websocket.MessageText, body); err != nil {
		s.removePending(id)
		return nil, fmt.Errorf("write to extension: %w", err)
	}

	deadline, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	select {
	case data := <-ch:
		return data, nil
	case <-deadline.Done():
		s.removePending(id)
		return nil, fmt.Errorf("waiting for extension reply: %w", deadline.Err())
	}
}

func (s *Server) removePending(id string) {
	s.mu.Lock()
	delete(s.pending, id)
	s.mu.Unlock()
}
