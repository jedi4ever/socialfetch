// Package mcp wraps socialfetch's existing capabilities (fetch / search
// / ask / timeline / list / bridge_status) as Model Context Protocol
// tools, so the same Go binary can be installed as a Claude Desktop
// Extension (.mcpb) and driven via JSON-RPC over stdio instead of via
// shell-out.
//
// This package is purely additive — it consumes the public surface of
// internal/core and the platform packages without modifying them. The
// constructor takes already-built registries so this layer doesn't
// touch buildRegistries / buildAskers in cmd/socialfetch (which are
// package-main and unimportable anyway).
//
// Usage from cmd/socialfetch:
//
//	srv := mcp.NewServer(mcp.Config{
//	    Fetchers:           fetchers,
//	    Searchers:          searchers,
//	    Askers:             askers,
//	    Timelines:          timelines,
//	    DefaultAskChain:    []string{"perplexity", "grok", ...},
//	    DefaultSearchChain: []string{"perplexity", "tavily", ...},
//	    Version:            "0.2.0",
//	})
//	server.ServeStdio(srv)
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/patrickdebois/social-skills/internal/bridge"
	"github.com/patrickdebois/social-skills/internal/core"
)

// Config bundles everything the MCP server needs. All fields are
// non-optional — the caller already has them (cmd/socialfetch builds
// them from buildRegistries/buildAskers/etc.) so there's no win in
// hiding the dependency.
type Config struct {
	Fetchers           *core.Registry
	Searchers          *core.SearchRegistry
	Askers             *core.AskRegistry
	Timelines          *core.TimelineRegistry
	DefaultAskChain    []string
	DefaultSearchChain []string
	Version            string

	// BridgePort is the port the local browser bridge listens on.
	// The `bridge_status` tool probes /status on this port.
	BridgePort int
}

// NewServer builds an MCP server with all socialfetch tools registered.
// Call server.ServeStdio(...) on the returned *server.MCPServer to run
// it.
func NewServer(cfg Config) *server.MCPServer {
	s := server.NewMCPServer(
		"socialfetch",
		cfg.Version,
		server.WithToolCapabilities(false),
	)
	registerTools(s, cfg)
	return s
}

func registerTools(s *server.MCPServer, cfg Config) {
	addFetchTool(s, cfg)
	addSearchTool(s, cfg)
	addAskTool(s, cfg)
	addTimelineTool(s, cfg)
	addListProvidersTool(s, cfg)
	addBridgeStatusTool(s, cfg)
}

// openToolAudit opens the always-on global audit log scoped to an MCP
// tool invocation. The audit log destination matches what
// `socialfetch fetch` / `search` / `ask` etc. use from the CLI; the
// only difference is the cmd string is `mcp:<tool>` so an operator
// running `socialfetch monitor` can tell MCP-driven calls apart from
// shell-driven ones.
//
// We never attach a user-facing destination because stdio is the
// JSON-RPC channel — writing audit lines to stdout would corrupt the
// protocol stream. If you need to debug, tail the global audit file:
//
//	socialfetch monitor
func openToolAudit(toolName string) (*core.AuditLogger, func()) {
	cmd := "mcp:" + toolName
	globalW, closeGlobal, err := core.OpenGlobalAudit(cmd)
	audit := core.NewAuditLogger(nil)
	if err == nil && globalW != nil {
		audit.AttachGlobal(globalW)
	}
	return audit, func() {
		if closeGlobal != nil {
			closeGlobal()
		}
	}
}

// ---- fetch -----------------------------------------------------------

type fetchArgs struct {
	URL               string `json:"url"`
	IncludeComments   *bool  `json:"include_comments,omitempty"`
	MaxComments       int    `json:"max_comments,omitempty"`
	GenericExtraction bool   `json:"generic_extraction,omitempty"`
}

func addFetchTool(s *server.MCPServer, cfg Config) {
	tool := mcp.NewTool("fetch",
		mcp.WithDescription("Fetch content at a URL — auto-detects the source (HackerNews, Reddit, GitHub, X/Twitter, LinkedIn, YouTube, Bluesky, arXiv, Medium, Substack, RSS, generic article). Returns a structured Item with title, author, content, comments, etc."),
		mcp.WithString("url", mcp.Required(), mcp.Description("The URL to fetch")),
		mcp.WithBoolean("include_comments", mcp.Description("Include comment trees (default true; set false for faster/smaller fetch)")),
		mcp.WithNumber("max_comments", mcp.Description("Cap total comments per item (0 = no cap)")),
		mcp.WithBoolean("generic_extraction", mcp.Description("Force the catch-all article extractor even on hosts with a specific extractor (debug aid)")),
	)
	s.AddTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, args fetchArgs) (*mcp.CallToolResult, error) {
		audit, closeAudit := openToolAudit("fetch")
		defer closeAudit()
		ctx = core.WithAudit(ctx, audit)
		audit.Logf("fetch %s", args.URL)

		if strings.TrimSpace(args.URL) == "" {
			audit.Logf("fetch FAILED: url is required")
			return mcp.NewToolResultError("url is required"), nil
		}
		opts := core.Options{
			IncludeComments:   args.IncludeComments == nil || *args.IncludeComments,
			MaxComments:       args.MaxComments,
			GenericExtraction: args.GenericExtraction,
			Audit:             audit,
		}
		item, err := cfg.Fetchers.Fetch(ctx, args.URL, opts)
		if err != nil {
			audit.Logf("fetch FAILED: %v", err)
			return mcp.NewToolResultError(err.Error()), nil
		}
		audit.Logf("fetch ok via %s kind=%s title=%q", item.Source, item.Kind, item.Title)
		return jsonResult(item)
	}))
}

// ---- search ----------------------------------------------------------

type searchArgs struct {
	Query    string `json:"query"`
	Provider string `json:"provider,omitempty"`
	Max      int    `json:"max,omitempty"`
	After    string `json:"after,omitempty"`
	Before   string `json:"before,omitempty"`
	Last     string `json:"last,omitempty"`
	Site     string `json:"site,omitempty"`
}

func addSearchTool(s *server.MCPServer, cfg Config) {
	tool := mcp.NewTool("search",
		mcp.WithDescription("Run a search query. Provider names: duckduckgo (default for unauthed), google, brave, serpapi, tavily, perplexity, hackernews, reddit, twitter, youtube, bluesky, arxiv. Special values: \"auto\" walks the default fallback chain; \"name1,name2\" tries each in order."),
		mcp.WithString("query", mcp.Required(), mcp.Description("Search query")),
		mcp.WithString("provider", mcp.Description("Provider name, \"auto\", or comma-separated chain (default: auto)")),
		mcp.WithNumber("max", mcp.Description("Max results (default 10)")),
		mcp.WithString("after", mcp.Description("Only results after this date (yyyy-mm-dd or RFC3339)")),
		mcp.WithString("before", mcp.Description("Only results before this date")),
		mcp.WithString("last", mcp.Description("Sugar for `after`: \"7d\", \"24h\", \"1m\"")),
		mcp.WithString("site", mcp.Description("Restrict to domain (single domain; comma-separate for multiple)")),
	)
	s.AddTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, args searchArgs) (*mcp.CallToolResult, error) {
		audit, closeAudit := openToolAudit("search")
		defer closeAudit()
		ctx = core.WithAudit(ctx, audit)
		audit.Logf("search %q via %s (max=%d)", args.Query, args.Provider, args.Max)

		if strings.TrimSpace(args.Query) == "" {
			audit.Logf("search FAILED: query is required")
			return mcp.NewToolResultError("query is required"), nil
		}
		provider, err := resolveSearcher(cfg, args.Provider)
		if err != nil {
			audit.Logf("search FAILED: %v", err)
			return mcp.NewToolResultError(err.Error()), nil
		}
		opts := core.SearchOptions{Max: args.Max}
		if t, err := parseDate(args.After); err != nil {
			return mcp.NewToolResultError("after: " + err.Error()), nil
		} else if t != nil {
			opts.After = t
		}
		if t, err := parseDate(args.Before); err != nil {
			return mcp.NewToolResultError("before: " + err.Error()), nil
		} else if t != nil {
			opts.Before = t
		}
		if args.Last != "" {
			d, err := parseLast(args.Last)
			if err != nil {
				return mcp.NewToolResultError("last: " + err.Error()), nil
			}
			t := time.Now().Add(-d)
			opts.After = &t
		}
		if args.Site != "" {
			for _, d := range strings.Split(args.Site, ",") {
				if d = strings.TrimSpace(d); d != "" {
					opts.IncludeDomains = append(opts.IncludeDomains, d)
				}
			}
		}
		results, err := provider.Search(ctx, args.Query, opts)
		if err != nil {
			audit.Logf("search FAILED: %v", err)
			return mcp.NewToolResultError(err.Error()), nil
		}
		audit.Logf("search returned %d results via %s", len(results), provider.Name())
		return jsonResult(map[string]any{
			"query":    args.Query,
			"provider": provider.Name(),
			"count":    len(results),
			"results":  results,
		})
	}))
}

// ---- ask -------------------------------------------------------------

type askArgs struct {
	Question     string `json:"question"`
	Provider     string `json:"provider,omitempty"`
	Model        string `json:"model,omitempty"`
	Recency      string `json:"recency,omitempty"`
	MaxTokens    int    `json:"max_tokens,omitempty"`
	Instructions string `json:"instructions,omitempty"`
}

func addAskTool(s *server.MCPServer, cfg Config) {
	tool := mcp.NewTool("ask",
		mcp.WithDescription("Ask a question of a grounded answer engine. Returns a synthesized answer plus citations. Provider names: perplexity, grok, openai, anthropic, google, tavily, serpapi. Special values: \"auto\" walks the default fallback chain; \"name1,name2\" tries each in order."),
		mcp.WithString("question", mcp.Required(), mcp.Description("The question to ask")),
		mcp.WithString("provider", mcp.Description("Provider name, \"auto\", or comma-separated chain (default: auto)")),
		mcp.WithString("model", mcp.Description("Override the provider's default model (empty = provider picks where supported)")),
		mcp.WithString("recency", mcp.Description("Search horizon: day, week, month, year (provider-dependent)")),
		mcp.WithNumber("max_tokens", mcp.Description("Cap response length")),
		mcp.WithString("instructions", mcp.Description("System-prompt-style preamble (honored by perplexity, grok, openai, anthropic, google)")),
	)
	s.AddTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, args askArgs) (*mcp.CallToolResult, error) {
		audit, closeAudit := openToolAudit("ask")
		defer closeAudit()
		ctx = core.WithAudit(ctx, audit)
		audit.Logf("ask %q via %s (model=%s, recency=%s)", args.Question, args.Provider, args.Model, args.Recency)

		if strings.TrimSpace(args.Question) == "" {
			audit.Logf("ask FAILED: question is required")
			return mcp.NewToolResultError("question is required"), nil
		}
		asker, err := resolveAsker(cfg, args.Provider)
		if err != nil {
			audit.Logf("ask FAILED: %v", err)
			return mcp.NewToolResultError(err.Error()), nil
		}
		ans, err := asker.Ask(ctx, args.Question, core.AskOptions{
			Model:        args.Model,
			Recency:      args.Recency,
			MaxTokens:    args.MaxTokens,
			Instructions: args.Instructions,
		})
		if err != nil {
			audit.Logf("ask FAILED: %v", err)
			return mcp.NewToolResultError(err.Error()), nil
		}
		audit.Logf("ask returned answer (%d chars, %d sources) via %s", len(ans.Text), len(ans.Sources), ans.Provider)
		return jsonResult(ans)
	}))
}

// ---- timeline --------------------------------------------------------

type timelineArgs struct {
	User          string `json:"user"`
	Provider      string `json:"provider,omitempty"`
	Kind          string `json:"kind,omitempty"`
	Max           int    `json:"max,omitempty"`
	Last          string `json:"last,omitempty"`
	Expand        bool   `json:"expand,omitempty"`
	ExcludeShares bool   `json:"no_reshares,omitempty"`
}

func addTimelineTool(s *server.MCPServer, cfg Config) {
	tool := mcp.NewTool("timeline",
		mcp.WithDescription("Recent activity for a user on X/Twitter or LinkedIn. Accepts bare handles (default to X), @-prefixed handles, or full profile URLs (auto-detected). LinkedIn requires the local browser bridge."),
		mcp.WithString("user", mcp.Required(), mcp.Description("User handle or profile URL")),
		mcp.WithString("provider", mcp.Description("x or linkedin; auto-detected from URL")),
		mcp.WithString("kind", mcp.Description("x: all, tweets, replies, retweets. linkedin: all, posts, comments, reactions.")),
		mcp.WithNumber("max", mcp.Description("Max items (default 30)")),
		mcp.WithString("last", mcp.Description("Sugar for after-window: \"7d\", \"24h\". X has a 7-day cap.")),
		mcp.WithBoolean("expand", mcp.Description("LinkedIn: deep-fetch each post (slower, fuller content)")),
		mcp.WithBoolean("no_reshares", mcp.Description("LinkedIn: drop reposts from the timeline")),
	)
	s.AddTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, args timelineArgs) (*mcp.CallToolResult, error) {
		audit, closeAudit := openToolAudit("timeline")
		defer closeAudit()
		ctx = core.WithAudit(ctx, audit)
		audit.Logf("timeline %s/%s (kind=%s, max=%d)", args.Provider, args.User, args.Kind, args.Max)

		if strings.TrimSpace(args.User) == "" {
			audit.Logf("timeline FAILED: user is required")
			return mcp.NewToolResultError("user is required"), nil
		}
		provider, user, err := core.ParseIdentifier(args.User, args.Provider)
		if err != nil {
			audit.Logf("timeline FAILED: %v", err)
			return mcp.NewToolResultError(err.Error()), nil
		}
		p, err := cfg.Timelines.Get(provider)
		if err != nil {
			audit.Logf("timeline FAILED: %v", err)
			return mcp.NewToolResultError(err.Error()), nil
		}
		opts := core.TimelineOptions{
			Kind:          args.Kind,
			Max:           args.Max,
			Expand:        args.Expand,
			ExcludeShares: args.ExcludeShares,
		}
		if args.Last != "" {
			d, err := parseLast(args.Last)
			if err != nil {
				return mcp.NewToolResultError("last: " + err.Error()), nil
			}
			t := time.Now().Add(-d)
			opts.After = &t
		}
		item, err := p.Fetch(ctx, user, opts)
		if err != nil {
			audit.Logf("timeline FAILED: %v", err)
			return mcp.NewToolResultError(err.Error()), nil
		}
		audit.Logf("timeline returned %d items for %s/%s", len(item.Children), provider, user)
		return jsonResult(item)
	}))
}

// ---- list_providers --------------------------------------------------

func addListProvidersTool(s *server.MCPServer, cfg Config) {
	tool := mcp.NewTool("list_providers",
		mcp.WithDescription("List all available fetch / search / ask / timeline providers. Useful for the agent to discover capabilities at runtime."),
	)
	s.AddTool(tool, func(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		audit, closeAudit := openToolAudit("list_providers")
		defer closeAudit()
		audit.Logf("list_providers called")

		fetchNames := make([]string, 0)
		for _, f := range cfg.Fetchers.Fetchers() {
			fetchNames = append(fetchNames, f.Name())
		}
		return jsonResult(map[string]any{
			"fetch":    fetchNames,
			"search":   cfg.Searchers.Names(),
			"ask":      cfg.Askers.Names(),
			"timeline": []string{"x", "linkedin"},
		})
	})
}

// ---- bridge_status ---------------------------------------------------

func addBridgeStatusTool(s *server.MCPServer, cfg Config) {
	tool := mcp.NewTool("bridge_status",
		mcp.WithDescription("Probe the local browser-extension bridge. Returns {reachable, connected, port}. LinkedIn / Medium / Substack fetches require this to be reachable + connected."),
	)
	s.AddTool(tool, func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		audit, closeAudit := openToolAudit("bridge_status")
		defer closeAudit()

		port := cfg.BridgePort
		if port == 0 {
			port = bridge.DefaultPort
		}
		url := fmt.Sprintf("http://127.0.0.1:%d/status", port)
		probe, cancel := context.WithTimeout(ctx, 1*time.Second)
		defer cancel()
		req, _ := http.NewRequestWithContext(probe, http.MethodGet, url, nil)
		resp, err := core.HTTPClient.Do(req)
		if err != nil {
			audit.Logf("bridge_status: not reachable on :%d", port)
			return jsonResult(map[string]any{"reachable": false, "connected": false, "port": port})
		}
		defer resp.Body.Close()
		var body struct {
			Connected bool `json:"connected"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&body)
		audit.Logf("bridge_status: reachable=%v connected=%v port=%d", resp.StatusCode == 200, body.Connected, port)
		return jsonResult(map[string]any{
			"reachable": resp.StatusCode == 200,
			"connected": body.Connected,
			"port":      port,
		})
	})
}

// ---- helpers ---------------------------------------------------------

// resolveAsker mirrors cmd/socialfetch.resolveAsker. Duplicated rather
// than exported because the chain config differs by caller and the
// function is tiny.
func resolveAsker(cfg Config, expr string) (core.Asker, error) {
	expr = strings.TrimSpace(expr)
	if expr == "" || strings.EqualFold(expr, "auto") {
		return core.NewAskChain(cfg.Askers, cfg.DefaultAskChain)
	}
	if strings.Contains(expr, ",") {
		parts := splitTrim(expr, ",")
		return core.NewAskChain(cfg.Askers, parts)
	}
	return cfg.Askers.Get(expr)
}

func resolveSearcher(cfg Config, expr string) (core.SearchProvider, error) {
	expr = strings.TrimSpace(expr)
	if expr == "" || strings.EqualFold(expr, "auto") {
		return core.NewSearchChain(cfg.Searchers, cfg.DefaultSearchChain)
	}
	if strings.Contains(expr, ",") {
		parts := splitTrim(expr, ",")
		return core.NewSearchChain(cfg.Searchers, parts)
	}
	return cfg.Searchers.Get(expr)
}

func splitTrim(s, sep string) []string {
	out := make([]string, 0)
	for _, p := range strings.Split(s, sep) {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// jsonResult serialises any value as a JSON CallToolResult.
// Failure to marshal becomes an error result rather than a panic so a
// single bad item doesn't kill the server.
func jsonResult(v any) (*mcp.CallToolResult, error) {
	body, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return mcp.NewToolResultError("marshal: " + err.Error()), nil
	}
	return mcp.NewToolResultText(string(body)), nil
}

func parseDate(s string) (*time.Time, error) {
	if s == "" {
		return nil, nil
	}
	for _, layout := range []string{"2006-01-02", time.RFC3339, "2006-01-02T15:04:05Z"} {
		if t, err := time.Parse(layout, s); err == nil {
			u := t.UTC()
			return &u, nil
		}
	}
	return nil, fmt.Errorf("date %q must be yyyy-mm-dd or RFC3339", s)
}

func parseLast(s string) (time.Duration, error) {
	if d, err := time.ParseDuration(s); err == nil {
		return d, nil
	}
	if strings.HasSuffix(s, "d") {
		var n int
		_, err := fmt.Sscanf(strings.TrimSuffix(s, "d"), "%d", &n)
		if err == nil {
			return time.Duration(n) * 24 * time.Hour, nil
		}
	}
	return 0, fmt.Errorf("invalid duration %q (try 7d, 24h, 1m)", s)
}
