// Package mcphttp wraps mcp-go's Streamable HTTP transport with
// the routing tree the social-* binaries share: an unauth status
// probe on / and /health, the actual MCP protocol on /mcp +
// /mcp/, and a bearer-token gate when MCP_AUTH_TOKEN is set.
//
// Lifted out of cmd/social-fetch/mcp.go so social-agent and
// social-ledger can reuse the same shape without copy-pasting the
// auth + status + request-log middleware. Callers vary in three
// ways — service name (for the status JSON / WWW-Authenticate
// realm), bearer token source, and request-log destination — all
// of which are Options fields here.
//
// Intentionally has no dependency on internal/core (and therefore
// no audit-log surface). Callers that want richer logging wrap
// the package's Logger callback with their own writer.
package mcphttp

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/server"
)

// Options configures Serve. All fields are optional except Service
// (used in the status JSON and bearer realm).
type Options struct {
	// Service is the short identifier the server returns in
	// `GET / | /health` JSON ("social-agent-mcp", "social-ledger-mcp",
	// "social-fetch", …). Also used as the WWW-Authenticate realm.
	Service string

	// Version is surfaced in the status JSON. Empty omits the field.
	Version string

	// Token, when non-empty, is the bearer required on /mcp.
	// Accepted on Authorization: Bearer <token> OR ?token=<token>
	// (the latter for clients that can't set custom headers).
	// Empty disables auth — only safe for loopback listens.
	Token string

	// Logger receives one line per HTTP request when non-nil —
	// "method path from remote status=N in DUR". When nil, lines
	// go to stderr prefixed with the Service name. Pass a no-op
	// to silence; pass a tee to fan out to audit logs.
	Logger func(line string)

	// ExtraHandlers registers additional path patterns alongside
	// the standard / + /health + /mcp tree. Use this to bolt on
	// auxiliary endpoints — file-serving sidecars, debug probes,
	// etc. Each handler is wrapped by the same bearer-token gate
	// as /mcp when Token is set, so callers present the same
	// `Authorization: Bearer <token>` header. Path patterns
	// follow http.ServeMux rules (a trailing slash means subtree
	// match). Status routes (/ + /health) stay unauthenticated;
	// don't register patterns that overlap them.
	ExtraHandlers map[string]http.Handler

	// UnauthExtraHandlers is the same as ExtraHandlers but the
	// bearer-token gate is NOT applied. Use this when the handler
	// owns its own auth scheme — e.g. signed-URL endpoints where
	// the URL itself carries the proof of authorization
	// (HMAC-signed query string), so wrapping with bearer auth
	// would defeat the point. Caller is responsible for ensuring
	// the handler rejects unauthenticated requests on every path
	// it serves.
	UnauthExtraHandlers map[string]http.Handler
}

// Serve binds addr and runs the Streamable HTTP MCP server with
// the standard routing tree. Blocks until the listener errors.
//
// Routing:
//
//	GET / and GET /health → unauthenticated status JSON
//	* /mcp and * /mcp/    → Streamable HTTP, bearer-gated when
//	                        Token != ""
//	everything else       → 404
func Serve(addr string, srv *server.MCPServer, opts Options) error {
	return (&http.Server{
		Addr:              addr,
		Handler:           NewMux(srv, opts),
		ReadHeaderTimeout: 10 * time.Second,
	}).ListenAndServe()
}

// NewMux returns the routing tree without binding a listener. Use
// this when the caller wants to install additional middleware
// (e.g. global audit log) around the standard tree.
func NewMux(srv *server.MCPServer, opts Options) http.Handler {
	streamable := server.NewStreamableHTTPServer(srv)

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		writeStatusJSON(w, opts)
	})
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		writeStatusJSON(w, opts)
	})

	realm := opts.Service
	if realm == "" {
		realm = "mcp"
	}
	gate := func(h http.Handler) http.Handler {
		if opts.Token == "" {
			return h
		}
		return bearerAuth(opts.Token, realm, h)
	}

	mcpHandler := gate(http.Handler(streamable))
	mux.Handle("/mcp", mcpHandler)
	mux.Handle("/mcp/", mcpHandler)

	for pattern, h := range opts.ExtraHandlers {
		mux.Handle(pattern, gate(h))
	}
	for pattern, h := range opts.UnauthExtraHandlers {
		mux.Handle(pattern, h)
	}

	// Inject r.Host into r.Header so MCP tool handlers — which
	// only see the Header map — can derive a public base URL
	// from the inbound request. Go's stdlib stores the Host
	// separately from Header (RFC-correct), but mcp-go
	// (server/streamable_http.go) only forwards Header to tool
	// handlers; without this middleware they'd see an empty
	// Host and fall back to relative URLs. Outermost so it runs
	// before /health, /mcp, and ExtraHandlers alike.
	return wrapRequestLog(opts, injectHostHeader(mux))
}

// injectHostHeader copies r.Host into r.Header["Host"] and
// reflects the actual transport (https vs http, taken from
// r.TLS) into r.Header["X-Forwarded-Proto"]. MCP tool handlers
// only see the Header map, never the http.Request fields, so
// without this injection they'd see an empty Host and assume
// HTTP — breaking absolute artifact URLs. Idempotent: if a
// fronting proxy already set Host or X-Forwarded-Proto, we
// preserve those (the proxy knows the public-facing transport
// better than we do).
func injectHostHeader(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "" && r.Header.Get("Host") == "" {
			r.Header.Set("Host", r.Host)
		}
		if r.Header.Get("X-Forwarded-Proto") == "" {
			scheme := "http"
			if r.TLS != nil {
				scheme = "https"
			}
			r.Header.Set("X-Forwarded-Proto", scheme)
		}
		next.ServeHTTP(w, r)
	})
}

// writeStatusJSON answers / and /health with a small advert: who
// we are, whether auth is required, where the protocol endpoint
// lives. No secrets — same response for every caller.
func writeStatusJSON(w http.ResponseWriter, opts Options) {
	w.Header().Set("Content-Type", "application/json")
	body := map[string]any{
		"service":       opts.Service,
		"mcp_endpoint":  "/mcp",
		"transport":     "streamable-http",
		"auth_required": opts.Token != "",
	}
	if opts.Version != "" {
		body["version"] = opts.Version
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(body)
}

// bearerAuth gates next on the token. Accepts the standard
// Authorization header AND a ?token= query-string fallback for
// clients that can't set custom headers (a few hosted integration
// UIs); query-string callers leak the token into access logs, so
// prefer the header where possible.
func bearerAuth(token, realm string, next http.Handler) http.Handler {
	expectedHeader := "Bearer " + token
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got == expectedHeader {
			next.ServeHTTP(w, r)
			return
		}
		if got := r.URL.Query().Get("token"); got != "" && got == token {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer realm=%q`, realm))
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
	})
}

// wrapRequestLog records per-request log lines and forwards to
// next. The query string is stripped before logging so ?token=...
// never reaches the log destination.
func wrapRequestLog(opts Options, next http.Handler) http.Handler {
	logFn := opts.Logger
	if logFn == nil {
		service := opts.Service
		if service == "" {
			service = "mcp"
		}
		logFn = func(line string) {
			fmt.Fprintln(os.Stderr, service+": "+line)
		}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &statusRecorder{ResponseWriter: w, status: 200}
		next.ServeHTTP(rw, r)
		dur := time.Since(start).Round(time.Millisecond)

		remote := r.Header.Get("X-Forwarded-For")
		if remote == "" {
			remote = r.RemoteAddr
		} else if i := strings.Index(remote, ","); i >= 0 {
			remote = strings.TrimSpace(remote[:i])
		}
		logFn(fmt.Sprintf("http %s %s from %s status=%d in %s",
			r.Method, r.URL.Path, remote, rw.status, dur))
	})
}

// statusRecorder captures the status code for the request log.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}
