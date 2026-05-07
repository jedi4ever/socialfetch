package main

// cmd_serve.go — HTTP listener + route registration for the
// `serve` subcommand. Embedded HTML/JS/CSS is served from / and
// /static/*; /api/* talks to the host social-agent MCP on the
// operator's behalf.

import (
	"flag"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"strings"
	"time"

	uiweb "github.com/jedi4ever/social-skills/cmd/social-ui/web"
)

func cmdServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	bind := fs.String("bind", "127.0.0.1:5571", "loopback bind addr (host:port)")
	agentURL := fs.String("agent-mcp-url", "", "host social-agent MCP HTTP endpoint. Empty = $SOCIAL_AGENT_MCP_URL, then http://127.0.0.1:5562/mcp.")
	if err := fs.Parse(args); err != nil {
		return err
	}

	resolved := strings.TrimSpace(*agentURL)
	if resolved == "" {
		resolved = strings.TrimSpace(os.Getenv("SOCIAL_AGENT_MCP_URL"))
	}
	if resolved == "" {
		resolved = "http://127.0.0.1:5562/mcp"
	}
	token := strings.TrimSpace(os.Getenv("MCP_AUTH_TOKEN"))

	api := newAPI(resolved, token)

	mux := http.NewServeMux()

	// Static frontend — embedded so a single binary ships with
	// the UI baked in, no install steps for HTML/JS/CSS.
	staticFS, err := buildStaticFS()
	if err != nil {
		return fmt.Errorf("embed: %w", err)
	}
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticFS))))
	mux.HandleFunc("/", api.serveIndex)

	// API routes — thin proxies over the host social-agent MCP.
	mux.HandleFunc("/api/sessions", api.handleSessionsCollection) // POST = create
	mux.HandleFunc("/api/sessions/", api.handleSessionsItem)      // DELETE /api/sessions/{sid} | POST /api/sessions/{sid}/runs | GET /api/sessions/{sid}/runs/{rid}
	mux.HandleFunc("/api/health", api.handleHealth)               // probe — useful when the operator's curl-debugging

	srv := &http.Server{
		Addr:              *bind,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	fmt.Fprintf(os.Stderr, "social-ui: listening on http://%s (agent MCP: %s)\n", *bind, resolved)
	return srv.ListenAndServe()
}

// buildStaticFS returns the embedded static directory rooted at
// "static" so http.StripPrefix("/static/") + http.FileServer maps
// correctly. The web/ embed directly; the index.html lives at the
// root and is served by handleIndex.
func buildStaticFS() (fs.FS, error) {
	return fs.Sub(uiweb.Files, "static")
}
