package mcp

// Ledger MCP tools — surface the social-ledger CLI as MCP tools
// under the social-fetch MCP server. Single tool catalog for the
// agent regardless of which binary actually does the work.
//
// Implementation pattern: each tool subprocesses to social-ledger
// with the appropriate subcommand and returns either the parsed
// JSON (for `seen` which has --format json) or the raw text output
// (for the rest — markdown / tabular forms the agent reads as-is).
//
// All tools share three failure cases:
//
//   1. social-ledger binary not available (Enabled() == false) →
//      "ledger not available — install social-ledger or set
//      SOCIAL_LEDGER=auto" so the agent can self-diagnose.
//   2. write tools (record, forget) when SOCIAL_LEDGER_READONLY=1
//      → ErrReadOnly's stable message so an agent can decide
//      whether to ask the user to flip the toggle.
//   3. subprocess-level failures (binary returned non-zero) →
//      surface stderr verbatim so platform-specific errors
//      (DB locked, permission denied, etc.) reach the agent.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/jedi4ever/social-skills/internal/core"
	"github.com/jedi4ever/social-skills/internal/ledger"
)

// runLedger is the shared subprocess invoker. Returns stdout on
// success; on failure returns an error whose Error() includes
// stderr so MCP error responses are diagnostic instead of
// "exit status 1".
//
// audit, when non-nil, gets a one-line entry per call so
// `social-fetch monitor` shows ledger MCP traffic alongside
// CLI / fetch / etc. invocations.
func runLedger(ctx context.Context, args []string, stdin string, audit *core.AuditLogger) ([]byte, error) {
	if !ledger.Enabled() {
		return nil, fmt.Errorf("ledger not available — install social-ledger and ensure it's on PATH (or set SOCIAL_LEDGER_BIN); or set SOCIAL_LEDGER=auto if you've explicitly disabled it via SOCIAL_LEDGER=0")
	}
	bin, err := ledgerBinaryPath()
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if dir := strings.TrimSpace(os.Getenv(ledger.DirEnv)); dir != "" {
		// Pass the storage dir through explicitly so the
		// subprocess doesn't depend on env inheritance order.
		args = append([]string{"--data-dir", dir}, args...)
		cmd = exec.CommandContext(ctx, bin, args...)
		if stdin != "" {
			cmd.Stdin = strings.NewReader(stdin)
		}
		stdout.Reset()
		stderr.Reset()
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
	}
	if audit != nil {
		audit.Logf("ledger %s", strings.Join(args, " "))
	}
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("social-ledger %s: %s", strings.Join(args, " "), msg)
	}
	return stdout.Bytes(), nil
}

// ledgerBinaryPath is the same lookup the Ingest path uses,
// re-exported here so the read-side tools can reuse it without
// duplicating the env / PATH / sibling-of-binary cascade. Lives
// next to runLedger so the call site reads top-down.
func ledgerBinaryPath() (string, error) {
	if explicit := strings.TrimSpace(os.Getenv(ledger.BinaryEnv)); explicit != "" {
		if _, err := os.Stat(explicit); err == nil {
			return explicit, nil
		}
		return "", fmt.Errorf("%s=%q does not exist", ledger.BinaryEnv, explicit)
	}
	if p, err := exec.LookPath("social-ledger"); err == nil {
		return p, nil
	}
	self, err := os.Executable()
	if err == nil {
		guess := strings.TrimSuffix(self, "/social-fetch") + "/social-ledger"
		if _, err := os.Stat(guess); err == nil {
			return guess, nil
		}
	}
	return "", fmt.Errorf("social-ledger not on $PATH (set %s or install via `go install ./cmd/social-ledger`)", ledger.BinaryEnv)
}

// ---- seen ------------------------------------------------------------

type ledgerSeenArgs struct {
	URLs []string `json:"urls"`
}

func addLedgerSeenTool(s *server.MCPServer, cfg Config) {
	tool := mcp.NewTool("social_ledger_seen",
		mcp.WithDescription("Check whether one or more URLs are already in the local ledger. Returns [{url, seen}, ...]. Use BEFORE fetching a URL to avoid re-fetching content the ledger already has cached. To learn whether a hit was auto-fetched by social_fetch_* (high trust — we extracted it ourselves) vs agent-recorded via social_ledger_record (trust depends on what was fed in), call social_ledger_get on the URL — it returns a `provenance` field."),
		mcp.WithArray("urls", mcp.Required(), mcp.Description("List of URLs to check. Each is matched literally + with trivial normalisation (lowercase host, strip fragment, trim trailing slash) + against both `url` and `request_url` columns so redirect short-links find their canonical entry.")),
	)
	s.AddTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, args ledgerSeenArgs) (*mcp.CallToolResult, error) {
		audit, closeAudit := openToolAudit(cfg, "ledger_seen")
		defer closeAudit()
		if len(args.URLs) == 0 {
			return mcp.NewToolResultError("urls is required and must be non-empty"), nil
		}
		argv := append([]string{"seen", "--format", "json"}, args.URLs...)
		out, err := runLedger(ctx, argv, "", audit)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		var results []map[string]any
		if err := json.Unmarshal(out, &results); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("ledger seen: parse json: %v\nraw: %s", err, out)), nil
		}
		return jsonResult(map[string]any{
			"count":   len(results),
			"results": results,
		})
	}))
}

// ---- get -------------------------------------------------------------

type ledgerGetArgs struct {
	URL    string `json:"url"`
	Inline bool   `json:"inline,omitempty"`
}

func addLedgerGetTool(s *server.MCPServer, cfg Config) {
	tool := mcp.NewTool("social_ledger_get",
		mcp.WithDescription("Retrieve one stored item from the ledger by URL or canonical_id. Default: writes the markdown body + frontmatter (source, url, author, score, tags, fetched_at) to a temp file and returns a small envelope with `content_file` + provenance fields. Read the body with the agent's Read tool (Claude Code) or social_fetch_read_file (Claude Desktop). Set `inline: true` to get the markdown directly. Use AFTER `social_ledger_seen` confirms a hit, or directly when you know the URL is cached.\n\nProvenance: the envelope's `provenance` field is `auto-fetched` when social_fetch_* ingested the content (we fetched + extracted it ourselves — high trust), or `agent-recorded` when an agent stored it via social_ledger_record (content came from Claude WebFetch / curl / hand-paste — trust depends on what the agent fed in)."),
		mcp.WithString("url", mcp.Required(), mcp.Description("URL or canonical_id of the stored item. Same fallback chain as the CLI: tries every (source::url) key, then last-ditch URL scan against both columns.")),
		mcp.WithBoolean("inline", mcp.Description("Return the markdown body inline instead of writing to a temp file. Default false.")),
	)
	s.AddTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, args ledgerGetArgs) (*mcp.CallToolResult, error) {
		audit, closeAudit := openToolAudit(cfg, "ledger_get")
		defer closeAudit()
		if strings.TrimSpace(args.URL) == "" {
			return mcp.NewToolResultError("url is required"), nil
		}
		out, err := runLedger(ctx, []string{"get", args.URL}, "", audit)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if args.Inline {
			return mcp.NewToolResultText(string(out)), nil
		}
		body := string(out)
		path, n, werr := writeContentTemp("ledger-get", "md", body)
		if werr != nil {
			return mcp.NewToolResultText(body), nil
		}
		// Parse the markdown frontmatter that `social-ledger get`
		// emits (Source/URL/Author/etc. lines) so the envelope
		// surfaces the headline metadata without forcing the agent
		// to read the file. Cheap and best-effort — missing fields
		// just stay absent.
		meta := parseLedgerFrontmatter(body)
		env := map[string]any{
			"url":           args.URL,
			"content_file":  path,
			"content_bytes": n,
			"provenance":    classifyProvenance(meta["source"]),
			"read_with":     "Claude Code: Read tool. Claude Desktop: social_fetch_read_file with this `content_file` as `path`.",
		}
		for k, v := range meta {
			if v != "" {
				env[k] = v
			}
		}
		return jsonResult(env)
	}))
}

// parseLedgerFrontmatter extracts the **Key:** value pairs that
// `social-ledger get` prints at the top of its markdown output. Stops
// at the first blank line — the renderer always emits one between
// frontmatter and body, so anything after that is content text we
// shouldn't re-interpret as metadata even if it happens to start with
// `**Source:`.
//
// Best effort — anything we can't parse stays empty rather than
// producing errors, since the file path is the source of truth.
func parseLedgerFrontmatter(md string) map[string]string {
	out := map[string]string{}
	seenFrontmatter := false
	for _, ln := range strings.SplitN(md, "\n", 40) {
		trimmed := strings.TrimSpace(ln)
		if !strings.HasPrefix(trimmed, "**") {
			if seenFrontmatter && trimmed == "" {
				// Blank line after the metadata block — body
				// follows. Bail before any body text starting
				// with `**…**` gets misread as frontmatter.
				break
			}
			continue
		}
		rest := strings.TrimPrefix(trimmed, "**")
		colon := strings.Index(rest, ":**")
		if colon < 0 {
			continue
		}
		seenFrontmatter = true
		key := strings.ToLower(strings.TrimSpace(rest[:colon]))
		val := strings.TrimSpace(rest[colon+3:])
		switch key {
		case "source", "author", "url", "published", "score", "tags", "fetched":
			out[key] = val
		}
	}
	return out
}

// classifyProvenance maps the ledger entry's Source value to a
// trust-class string the agent can read. The convention is:
//
//   - Platform sources (hackernews, reddit, github, x/twitter,
//     linkedin, youtube, bluesky, arxiv, medium, substack, rss,
//     article) — produced by social_fetch_* fetchers, so we know
//     extraction came from our own code on the live URL.
//
//   - `webfetch`, `manual`, `research-tool`, `citation`, `bridge` —
//     stored via `social_ledger_record` (agent-supplied content) or
//     captured indirectly. Trust depends on what the agent fed in.
//
// Anything else is reported as "unknown" so the agent can default to
// caution rather than silent assume-trust.
func classifyProvenance(source string) string {
	source = strings.TrimSpace(strings.ToLower(source))
	if source == "" {
		return "unknown"
	}
	switch source {
	case "hackernews", "reddit", "github", "x", "twitter", "linkedin",
		"youtube", "bluesky", "arxiv", "medium", "substack", "rss",
		"article", "atom":
		return "auto-fetched"
	case "webfetch", "manual", "research-tool", "research", "citation":
		return "agent-recorded"
	default:
		return "unknown"
	}
}

// ---- list ------------------------------------------------------------

type ledgerListArgs struct {
	Source string `json:"source,omitempty"`
	Since  string `json:"since,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}

func addLedgerListTool(s *server.MCPServer, cfg Config) {
	tool := mcp.NewTool("social_ledger_list",
		mcp.WithDescription("List ledger items, newest first. Filter by source (hackernews, reddit, citation, webfetch, …) and/or by recency window (e.g. `7d`, `24h`)."),
		mcp.WithString("source", mcp.Description("Filter by source string. Empty = all sources.")),
		mcp.WithString("since", mcp.Description("Recency window (`7d`, `24h`, `1m`). Empty = no time filter.")),
		mcp.WithNumber("limit", mcp.Description("Max items to return (default 25).")),
	)
	s.AddTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, args ledgerListArgs) (*mcp.CallToolResult, error) {
		audit, closeAudit := openToolAudit(cfg, "ledger_list")
		defer closeAudit()
		argv := []string{"list"}
		if args.Source != "" {
			argv = append(argv, "--source", args.Source)
		}
		if args.Since != "" {
			argv = append(argv, "--since", args.Since)
		}
		if args.Limit > 0 {
			argv = append(argv, "-n", fmt.Sprintf("%d", args.Limit))
		}
		out, err := runLedger(ctx, argv, "", audit)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultText(string(out)), nil
	}))
}

// ---- search ----------------------------------------------------------

type ledgerSearchArgs struct {
	Query string `json:"query"`
	Limit int    `json:"limit,omitempty"`
}

func addLedgerSearchTool(s *server.MCPServer, cfg Config) {
	tool := mcp.NewTool("social_ledger_search",
		mcp.WithDescription("Full-text search the ledger via SQLite FTS5 (BM25-ranked). Searches across title, summary, content, author, tags. Use to answer 'have we read anything about X?' / 'find every item that mentions Y'. Returns one line per hit with source, title, url, plus a snippet."),
		mcp.WithString("query", mcp.Required(), mcp.Description("FTS5 query. Supports phrase quoting (\"vercel ai\"), AND/OR/NOT, prefix* (`harness*`), NEAR(). Bare terms are AND'd by default.")),
		mcp.WithNumber("limit", mcp.Description("Max results (default 25).")),
	)
	s.AddTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, args ledgerSearchArgs) (*mcp.CallToolResult, error) {
		audit, closeAudit := openToolAudit(cfg, "ledger_search")
		defer closeAudit()
		if strings.TrimSpace(args.Query) == "" {
			return mcp.NewToolResultError("query is required"), nil
		}
		argv := []string{"search"}
		if args.Limit > 0 {
			argv = append(argv, "-n", fmt.Sprintf("%d", args.Limit))
		}
		argv = append(argv, args.Query)
		out, err := runLedger(ctx, argv, "", audit)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultText(string(out)), nil
	}))
}

// ---- stats -----------------------------------------------------------

func addLedgerStatsTool(s *server.MCPServer, cfg Config) {
	tool := mcp.NewTool("social_ledger_stats",
		mcp.WithDescription("Summary statistics for the ledger: total items, per-source counts, oldest/newest, disk usage. Cheap; use freely to sanity-check what the agent has access to."),
	)
	s.AddTool(tool, func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		audit, closeAudit := openToolAudit(cfg, "ledger_stats")
		defer closeAudit()
		out, err := runLedger(ctx, []string{"stats"}, "", audit)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultText(string(out)), nil
	})
}

// ---- record ----------------------------------------------------------

type ledgerRecordArgs struct {
	URL         string `json:"url"`
	Title       string `json:"title"`
	Content     string `json:"content,omitempty"`
	ContentFile string `json:"content_file,omitempty"`
	Source      string `json:"source,omitempty"`
	Summary     string `json:"summary,omitempty"`
	Author      string `json:"author,omitempty"`
	CanonicalID string `json:"canonical_id,omitempty"`
}

func addLedgerRecordTool(s *server.MCPServer, cfg Config) {
	tool := mcp.NewTool("social_ledger_record",
		mcp.WithDescription("Store content fetched via a NON-socialfetch path (Claude's WebFetch tool, the research tool, an ad-hoc curl, anything outside the social-fetch family) into the ledger so the next conversation finds it cached. DO NOT use this for content fetched via social_fetch_fetch / search / ask / timeline / research — those auto-ingest into the ledger automatically; calling record on top creates a duplicate row. Workflow: WebFetch → Write the markdown to /tmp/<slug>.md → record with content_file pointing at that path (PREFERRED — avoids streaming the body through MCP's JSON-escape and the agent's token budget). Inline `content` is OK for tiny bodies (one-line notes). Refused when SOCIAL_LEDGER_READONLY=1 is set."),
		mcp.WithString("url", mcp.Required(), mcp.Description("Source URL of the recorded content.")),
		mcp.WithString("title", mcp.Required(), mcp.Description("Page title (required — empty titles produce useless ledger entries).")),
		mcp.WithString("content_file", mcp.Description("PREFERRED for non-trivial bodies. Absolute path to a file on disk containing the markdown body. The agent should Write the WebFetch output to a temp file (e.g. /tmp/<slug>.md) and pass that path here. Mutually exclusive with `content` — if both set, content_file wins.")),
		mcp.WithString("content", mcp.Description("Inline markdown body. Use only for SHORT content (<5KB). For larger bodies, prefer content_file.")),
		mcp.WithString("source", mcp.Description("Source tag for filtering later (default `webfetch`). Use `research-tool`, `manual`, etc. for cleaner separation.")),
		mcp.WithString("summary", mcp.Description("Short description / abstract.")),
		mcp.WithString("author", mcp.Description("Author / organisation that produced the content.")),
		mcp.WithString("canonical_id", mcp.Description("Stable cross-platform ID. Defaults to URL if empty.")),
	)
	s.AddTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, args ledgerRecordArgs) (*mcp.CallToolResult, error) {
		audit, closeAudit := openToolAudit(cfg, "ledger_record")
		defer closeAudit()
		if ledger.ReadOnly() {
			return mcp.NewToolResultError(ledger.ErrReadOnly.Error()), nil
		}
		if strings.TrimSpace(args.URL) == "" {
			return mcp.NewToolResultError("url is required"), nil
		}
		if strings.TrimSpace(args.Title) == "" {
			return mcp.NewToolResultError("title is required"), nil
		}
		if strings.TrimSpace(args.Content) == "" && strings.TrimSpace(args.ContentFile) == "" {
			return mcp.NewToolResultError("either content_file (preferred) or content is required"), nil
		}
		argv := []string{"record"}
		if args.Source != "" {
			argv = append(argv, "--source", args.Source)
		}
		argv = append(argv, "--title", args.Title)
		if args.Summary != "" {
			argv = append(argv, "--summary", args.Summary)
		}
		if args.Author != "" {
			argv = append(argv, "--author", args.Author)
		}
		if args.CanonicalID != "" {
			argv = append(argv, "--canonical-id", args.CanonicalID)
		}
		// content_file wins when both are set — let the agent
		// optimise without us second-guessing.
		stdin := ""
		if args.ContentFile != "" {
			argv = append(argv, "--content", args.ContentFile)
		} else {
			stdin = args.Content
		}
		argv = append(argv, args.URL)
		out, err := runLedger(ctx, argv, stdin, audit)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultText(strings.TrimSpace(string(out)) + "\nrecorded successfully"), nil
	}))
}

// ---- forget ----------------------------------------------------------

type ledgerForgetArgs struct {
	URL string `json:"url"`
}

func addLedgerForgetTool(s *server.MCPServer, cfg Config) {
	tool := mcp.NewTool("social_ledger_forget",
		mcp.WithDescription("Delete one item from the ledger. Refused when SOCIAL_LEDGER_READONLY=1 is set. Use sparingly — there's no undo."),
		mcp.WithString("url", mcp.Required(), mcp.Description("URL or canonical_id of the item to drop.")),
	)
	s.AddTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, args ledgerForgetArgs) (*mcp.CallToolResult, error) {
		audit, closeAudit := openToolAudit(cfg, "ledger_forget")
		defer closeAudit()
		if ledger.ReadOnly() {
			return mcp.NewToolResultError(ledger.ErrReadOnly.Error()), nil
		}
		if strings.TrimSpace(args.URL) == "" {
			return mcp.NewToolResultError("url is required"), nil
		}
		out, err := runLedger(ctx, []string{"forget", args.URL}, "", audit)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultText(string(out)), nil
	}))
}
