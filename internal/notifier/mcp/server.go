// Package mcp exposes the notifier providers as one MCP tool —
// social_notifier_post. Mirrors internal/agent/mcp's wiring: stdio
// transport via server.ServeStdio; cmd/social-notifier/cmd_mcp.go
// wraps it with internal/util/mcphttp for the --http :PORT path
// (bearer-token-gated, same shape as social-agent / social-ledger
// mcp).
//
// One tool keeps the surface narrow on purpose. Provider name is
// an arg, channel is an arg — the tool isn't per-provider. As we
// add providers (discord, email, …) the tool stays the same; the
// only thing that changes is what shows up in
// `social_notifier_providers_list`.
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/jedi4ever/social-skills/internal/notifier"
)

// Config carries per-server settings. Mirrors the shape
// internal/agent/mcp uses.
type Config struct {
	Version string
}

// NewServer builds an MCP server with the notifier tools
// registered. Caller drives it via server.ServeStdio (stdio
// transport) or wraps the returned server in mcphttp for HTTP.
func NewServer(cfg Config) *server.MCPServer {
	s := server.NewMCPServer(
		"social-notifier",
		cfg.Version,
		server.WithToolCapabilities(false),
	)
	addPostTool(s)
	addProvidersListTool(s)
	return s
}

// ---- post -------------------------------------------------------

type postArgs struct {
	Provider string `json:"provider,omitempty"`
	Channel  string `json:"channel,omitempty"`
	Text     string `json:"text,omitempty"`
	Blocks   string `json:"blocks,omitempty"`
	ThreadID string `json:"thread_id,omitempty"`
}

func addPostTool(s *server.MCPServer) {
	tool := mcpgo.NewTool("social_notifier_post",
		mcpgo.WithDescription("Send a single short notification via a registered provider (Slack today). Returns `{id, permalink?}` — `id` is the platform's stable message id (Slack ts), suitable to pass back as `thread_id` for follow-up replies. Use this from a long-running agent run to ping the operator's Slack channel when a report is ready, when a sub-task completes, or when the agent hits a question that needs a human. Don't use it for chatty progress updates — the operator's channel is theirs, not the agent's status feed."),
		mcpgo.WithString("provider", mcpgo.Description("Provider name (today: 'slack'). Empty = first registered. See social_notifier_providers_list.")),
		mcpgo.WithString("channel", mcpgo.Description("Channel id or name (provider-scoped). Empty = provider default (e.g. SLACK_DEFAULT_CHANNEL).")),
		mcpgo.WithString("text", mcpgo.Description("Plain-text message body. Always sent — falls back to this in clients that don't render structured blocks (mobile push, screen readers).")),
		mcpgo.WithString("blocks", mcpgo.Description("Optional structured payload as raw JSON (Slack blocks array, Discord embeds, …). Provider-specific shape — caller is responsible for it being valid.")),
		mcpgo.WithString("thread_id", mcpgo.Description("Reply to this earlier message id (Slack ts) so a long-running run's status updates collapse into one thread instead of spamming the channel. Empty = top-level post.")),
	)
	s.AddTool(tool, mcpgo.NewTypedToolHandler(func(ctx context.Context, _ mcpgo.CallToolRequest, args postArgs) (*mcpgo.CallToolResult, error) {
		if strings.TrimSpace(args.Text) == "" && args.Blocks == "" {
			return mcpgo.NewToolResultError("text or blocks required"), nil
		}
		prov, err := notifier.Get(args.Provider)
		if err != nil {
			return mcpgo.NewToolResultError(err.Error()), nil
		}
		opts := notifier.PostOpts{
			Channel:  args.Channel,
			Text:     args.Text,
			ThreadID: args.ThreadID,
		}
		if args.Blocks != "" {
			// Validate JSON syntax before shipping; saves a
			// round-trip when the agent generates malformed
			// blocks. Provider-specific schema validation
			// happens upstream.
			var probe any
			if err := json.Unmarshal([]byte(args.Blocks), &probe); err != nil {
				return mcpgo.NewToolResultError("blocks: invalid JSON: " + err.Error()), nil
			}
			opts.Blocks = []byte(args.Blocks)
		}
		res, err := prov.Post(ctx, opts)
		if err != nil {
			return mcpgo.NewToolResultError(err.Error()), nil
		}
		body, _ := json.Marshal(res)
		return mcpgo.NewToolResultText(string(body)), nil
	}))
}

// ---- providers list --------------------------------------------

func addProvidersListTool(s *server.MCPServer) {
	tool := mcpgo.NewTool("social_notifier_providers_list",
		mcpgo.WithDescription("List the notifier providers this MCP supports — slack today, discord/email/webhook/pagerduty future. Returns an array of names; pass any of them as `provider` on social_notifier_post."),
	)
	s.AddTool(tool, mcpgo.NewTypedToolHandler(func(_ context.Context, _ mcpgo.CallToolRequest, _ struct{}) (*mcpgo.CallToolResult, error) {
		body, _ := json.Marshal(notifier.Names())
		return mcpgo.NewToolResultText(string(body)), nil
	}))
}

// ensure we keep the fmt import even when no error formatting is
// needed inline — placeholder for richer error messages later.
var _ = fmt.Sprintf
