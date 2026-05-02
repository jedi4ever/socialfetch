// MCP subcommand: runs an MCP server in one of two transports:
//
//   - stdio (default) — for Claude Desktop Extension (.mcpb) installs
//     where Claude Desktop launches the binary as a subprocess and
//     speaks JSON-RPC on stdin/stdout.
//
//   - HTTP/streamable (--http :PORT) — for remote MCP clients like
//     claude.ai's Custom Connectors. Pair with `ngrok http PORT`
//     during local development to get a public HTTPS URL claude.ai
//     can reach without standing up cloud infra.
//
// All tool handlers live in internal/mcp; this file is just the entry
// point that builds the registries and hands them to the server.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/mark3labs/mcp-go/server"

	"github.com/patrickdebois/social-skills/internal/bridge"
	"github.com/patrickdebois/social-skills/internal/core"
	"github.com/patrickdebois/social-skills/internal/mcp"
	"github.com/patrickdebois/social-skills/internal/platforms/linkedin"
	"github.com/patrickdebois/social-skills/internal/platforms/twitter"
)

func runMCP(args []string) error {
	var (
		httpAddr  string
		useNgrok  bool
	)
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch a {
		case "-h", "--help":
			printMCPHelp()
			return nil
		case "--http":
			i++
			if i >= len(args) {
				return fmt.Errorf("mcp: --http needs a value (e.g. :8080)")
			}
			httpAddr = args[i]
			// Allow bare "8080" as shorthand for ":8080".
			if !strings.Contains(httpAddr, ":") {
				httpAddr = ":" + httpAddr
			}
		case "--ngrok":
			useNgrok = true
			// --ngrok implies --http; default to :8080 when the
			// user didn't supply one.
			if httpAddr == "" {
				httpAddr = ":8080"
			}
		default:
			return fmt.Errorf("mcp: unknown argument %q", a)
		}
	}

	fetchers, searchers := buildRegistries()
	askers := buildAskers()
	timelines := core.NewTimelineRegistry(
		twitter.NewXProvider(twitter.NewSearchProvider()),
		linkedin.NewLinkedInProvider(),
	)

	srv := mcp.NewServer(mcp.Config{
		Fetchers:           fetchers,
		Searchers:          searchers,
		Askers:             askers,
		Timelines:          timelines,
		DefaultAskChain:    defaultAskChain,
		DefaultSearchChain: defaultSearchChain,
		Version:            Version,
		BridgePort:         bridge.DefaultPort,
	})

	if httpAddr != "" {
		return runMCPOverHTTP(srv, httpAddr, useNgrok)
	}
	// ServeStdio reads JSON-RPC from os.Stdin and writes it to
	// os.Stdout. Anything we log on stdout corrupts the protocol —
	// the audit logger always writes to a file or stderr, so it's safe.
	return server.ServeStdio(srv)
}

// runMCPOverHTTP serves the MCP protocol over Streamable HTTP. The
// `MCP_AUTH_TOKEN` env var, if set, gates every request — clients
// must send `Authorization: Bearer <token>` (or `?token=<token>`).
// Use a long random string for ngrok-tunneled deployments where
// the URL is otherwise crawlable. Empty token = no auth (only safe
// for localhost-only listens like 127.0.0.1:8080).
//
// When useNgrok=true:
//   - If MCP_AUTH_TOKEN isn't set we generate a 32-byte hex token
//     so the public ngrok URL isn't unauthenticated.
//   - The HTTP server runs in a goroutine.
//   - We spawn `ngrok http <port>` as a subprocess.
//   - Poll ngrok's local agent API (127.0.0.1:4040) for the public
//     HTTPS URL, then print everything the user needs to paste
//     into claude.ai's Custom Connector setup.
//   - On Ctrl+C, kill the ngrok child cleanly.
func runMCPOverHTTP(mcpSrv *server.MCPServer, addr string, useNgrok bool) error {
	streamable := server.NewStreamableHTTPServer(mcpSrv)
	token := strings.TrimSpace(os.Getenv("MCP_AUTH_TOKEN"))
	tokenSource := "MCP_AUTH_TOKEN env"
	if token == "" && useNgrok {
		// Public URL needs auth — generate one for the user.
		token = randomHex(32)
		tokenSource = "auto-generated (--ngrok)"
	}
	handler := http.Handler(streamable)
	if token != "" {
		handler = bearerAuth(token, handler)
		fmt.Fprintf(os.Stderr, "socialfetch mcp: bearer-token auth enabled (%s)\n", tokenSource)
	} else {
		fmt.Fprintf(os.Stderr,
			"socialfetch mcp: WARNING — no MCP_AUTH_TOKEN set, every request accepted unauthenticated.\n"+
				"  Set MCP_AUTH_TOKEN before exposing the listener via ngrok or any public URL.\n")
	}
	httpSrv := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}
	fmt.Fprintf(os.Stderr, "socialfetch mcp: listening on %s (Streamable HTTP)\n", addr)

	if !useNgrok {
		return httpSrv.ListenAndServe()
	}

	// --ngrok mode: serve in a goroutine, spawn ngrok, print URL.
	errCh := make(chan error, 1)
	go func() { errCh <- httpSrv.ListenAndServe() }()

	port := portFromAddr(addr)
	ngrokCmd, publicURL, err := startNgrok(port)
	if err != nil {
		_ = httpSrv.Close()
		return fmt.Errorf("ngrok: %w", err)
	}
	defer func() {
		if ngrokCmd != nil && ngrokCmd.Process != nil {
			_ = ngrokCmd.Process.Signal(syscall.SIGTERM)
			_, _ = ngrokCmd.Process.Wait()
		}
	}()

	printNgrokInstructions(publicURL, token)

	// Wait for either Ctrl+C or HTTP server failure.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	select {
	case <-sig:
		fmt.Fprintln(os.Stderr, "\nsocialfetch mcp: shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutdownCtx)
		return nil
	case err := <-errCh:
		return err
	}
}

// startNgrok spawns `ngrok http <port>` as a child process and
// blocks until ngrok's local agent API reports the tunnel is up,
// returning the *exec.Cmd handle (so the caller can stop it on
// shutdown) plus the public HTTPS URL.
//
// Failure modes worth surfacing clearly:
//   - ngrok binary not on PATH → install hint
//   - tunnel never comes up within ~10s → likely a missing
//     `ngrok config add-authtoken …` (free tier requires it)
func startNgrok(port int) (*exec.Cmd, string, error) {
	if _, err := exec.LookPath("ngrok"); err != nil {
		return nil, "", fmt.Errorf(
			"ngrok not found on PATH. Install: brew install ngrok (macOS), or see https://ngrok.com/download. Then run `ngrok config add-authtoken <your-token>` once.",
		)
	}
	cmd := exec.Command("ngrok", "http", fmt.Sprintf("%d", port), "--log=stdout", "--log-level=warn")
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		return nil, "", fmt.Errorf("spawn ngrok: %w", err)
	}
	url, err := waitForNgrokTunnel(10 * time.Second)
	if err != nil {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		_, _ = cmd.Process.Wait()
		return nil, "", err
	}
	return cmd, url, nil
}

// waitForNgrokTunnel polls ngrok's local API at 127.0.0.1:4040 until
// it returns at least one tunnel with a public HTTPS URL, or the
// timeout trips.
func waitForNgrokTunnel(timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		time.Sleep(300 * time.Millisecond)
		resp, err := http.Get("http://127.0.0.1:4040/api/tunnels")
		if err != nil {
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		var data struct {
			Tunnels []struct {
				PublicURL string `json:"public_url"`
			} `json:"tunnels"`
		}
		if err := json.Unmarshal(body, &data); err != nil {
			continue
		}
		for _, t := range data.Tunnels {
			if strings.HasPrefix(t.PublicURL, "https://") {
				return t.PublicURL, nil
			}
		}
	}
	return "", fmt.Errorf("ngrok tunnel didn't come up within %s — check `ngrok config add-authtoken <token>` is set, or run `ngrok http %d` directly to see the actual error", timeout, 0)
}

// printNgrokInstructions writes the claude.ai-ready connector setup
// instructions to stderr (stdout is reserved for any future MCP
// stdio behavior). Plain-text on purpose so the user can copy/paste.
func printNgrokInstructions(publicURL, token string) {
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "──────────────────────────────────────────────────────────────")
	fmt.Fprintln(os.Stderr, "  socialfetch MCP server is live via ngrok.")
	fmt.Fprintln(os.Stderr)
	fmt.Fprintf(os.Stderr,  "  URL:    %s/mcp\n", publicURL)
	if token != "" {
		fmt.Fprintf(os.Stderr, "  Token:  %s\n", token)
	}
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "  Add to claude.ai → Settings → Connectors → Add custom")
	fmt.Fprintln(os.Stderr, "  connector:")
	fmt.Fprintf(os.Stderr,  "    1. Connector URL:  %s/mcp\n", publicURL)
	if token != "" {
		fmt.Fprintln(os.Stderr, "    2. Authentication:  Bearer token (paste the token above)")
		fmt.Fprintf(os.Stderr,  "       Or use this URL with embedded token:\n")
		fmt.Fprintf(os.Stderr,  "         %s/mcp?token=%s\n", publicURL, token)
	}
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "  Ctrl+C to stop the server and tear down the tunnel.")
	fmt.Fprintln(os.Stderr, "──────────────────────────────────────────────────────────────")
	fmt.Fprintln(os.Stderr)
}

// portFromAddr extracts the numeric port from "host:port" or ":port".
// Returns 0 on parse failure (callers default to 8080 upstream).
func portFromAddr(addr string) int {
	colon := strings.LastIndex(addr, ":")
	if colon < 0 {
		return 0
	}
	var p int
	_, err := fmt.Sscanf(addr[colon+1:], "%d", &p)
	if err != nil {
		return 0
	}
	return p
}

// randomHex returns 2*n hex chars of crypto-grade randomness.
// Suitable for short-lived bearer tokens: 32 bytes = 256 bits, way
// more than needed but cheap.
func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	return hex.EncodeToString(b)
}

// bearerAuth gates `next` on the supplied token. Accepts the token
// in EITHER place:
//
//   - `Authorization: Bearer <token>` header — preferred, the
//     standard MCP/HTTP auth shape. claude.ai's Custom Connector
//     UI fills this in automatically when you paste a token in the
//     auth field.
//
//   - `?token=<token>` query string — fallback for clients that
//     can't set custom headers (some integration UIs don't support
//     them on user-supplied connectors). The token is logged to
//     server access logs whenever query-string auth is used, so
//     prefer the header where possible.
//
// Sends 401 with WWW-Authenticate on missing/wrong creds so claude.ai
// surfaces a clear "this connector needs auth" message.
func bearerAuth(expected string, next http.Handler) http.Handler {
	expectedHeader := "Bearer " + expected
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 1. Header path
		if got := r.Header.Get("Authorization"); got == expectedHeader {
			next.ServeHTTP(w, r)
			return
		}
		// 2. Query-string fallback
		if got := r.URL.Query().Get("token"); got != "" && got == expected {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("WWW-Authenticate", `Bearer realm="socialfetch-mcp"`)
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
	})
}

func printMCPHelp() {
	fmt.Fprint(os.Stdout, `socialfetch mcp — run an MCP server (stdio or HTTP)

Usage:
  socialfetch mcp                       # stdio (Claude Desktop Extension)
  socialfetch mcp --http :8080          # Streamable HTTP (claude.ai, ngrok)

Stdio mode is what Claude Desktop launches when you install the
.mcpb extension; the server speaks JSON-RPC on stdin/stdout. Don't
type into a stdio MCP server directly — it expects MCP framing.

HTTP mode (--http :PORT) serves the same tools over Streamable HTTP
for remote MCP clients like claude.ai's Custom Connectors.

Quickest path for local development — let socialfetch spawn ngrok
for you and print the connector URL + token:

  $ socialfetch mcp --ngrok                    # defaults to :8080
  $ socialfetch mcp --ngrok --http :9090       # override the port

Or run them yourself:

  $ MCP_AUTH_TOKEN=$(uuidgen) socialfetch mcp --http :8080
  $ ngrok http 8080

Then in claude.ai → Settings → Connectors → Add custom connector,
paste the ngrok URL and the token (claude.ai will send it as
"Authorization: Bearer <token>"). The query-string fallback
"?token=<token>" works too if the UI doesn't expose an auth header
field.

Configure API keys via env vars — same names the other subcommands
read (ANTHROPIC_API_KEY, OPENAI_API_KEY, etc.). The dotenv loader
picks up nearby .env files automatically.

Auth (HTTP mode only):
  MCP_AUTH_TOKEN     bearer token required for every request. Empty
                     = no auth (only safe for 127.0.0.1 listens).
                     Set this before exposing the listener via ngrok
                     or any public URL.

Flags:
  --http :PORT       run as Streamable HTTP server on the given
                     address (e.g. :8080, 127.0.0.1:8080)
  --ngrok            spawn ngrok automatically and print the public
                     URL + a generated bearer token. Defaults to
                     :8080; combine with --http :PORT to override.
  -h, --help         show this help
`)
}
