package mcp

// MCP tools for the influencer directory — `social_ledger_influencers_*`.
// Mirror the CLI subcommand shape one-to-one (list, get, add, remove,
// subscribe, unsubscribe) so agents and operators see the same surface
// across transports.
//
// All write tools (add, remove, subscribe, unsubscribe) honor
// SOCIAL_LEDGER_READONLY=1 so a sandboxed agent can browse the
// directory without risk of mutation. Reads are always allowed.
//
// Why dedicated tools rather than reusing the generic ledger tools
// with `source=influencer`: agents using these tools mid-research
// ("I just learned about Andrej Karpathy — track him for AI") need
// a focused, well-described surface that names the use case. The
// generic `social_ledger_list --source influencer` works but reads
// to the agent as "list rows of an internal table" instead of
// "manage the people I follow." First-class tools also let us
// shape the input for upserts (socials map, topics list) without
// the agent constructing JSONL by hand.

import (
	"context"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/jedi4ever/social-skills/internal/ledger"
	"github.com/jedi4ever/social-skills/internal/platforms/influencers"
)

// ---- list ------------------------------------------------------------

type influencersListArgs struct {
	Type         string  `json:"type,omitempty"`
	Topic        string  `json:"topic,omitempty"`
	Has          string  `json:"has,omitempty"`
	FollowedOnly bool    `json:"followed_only,omitempty"`
	Limit        flexInt `json:"limit,omitempty"`
}

func addInfluencersListTool(s *server.MCPServer, cfg Config) {
	tool := mcp.NewTool("social_ledger_influencers_list",
		mcp.WithDescription("List tracked influencers — people / companies the user has flagged as topic authorities. Each row carries name, type (person/company), socials map (linkedin/x/github/bluesky/website/...), topics they're known for, free-form description, and any subscribed channels (subscribe = 'refresh this person's X timeline when researching ai'). Use to answer 'who do I track for AI?' / 'what authorities are subscribed for harness?' / 'show me everyone I follow on X'. Returns sorted by name asc for stable output."),
		mcp.WithString("type", mcp.Description("Filter to 'person' or 'company'. Empty = both.")),
		mcp.WithString("topic", mcp.Description("Case-insensitive substring match across topics + subscribed-channel topics. E.g. 'ai' matches anyone known for AI or subscribed-for-AI on any channel.")),
		mcp.WithString("has", mcp.Description("Only entries that have a handle for this platform. E.g. 'bluesky' to find every tracked person whose bluesky is recorded.")),
		mcp.WithBoolean("followed_only", mcp.Description("True = only return influencers with at least one subscribed channel. Useful for 'whose feeds should I refresh?' workflows.")),
		mcp.WithNumber("limit", mcp.Description("Cap output at N rows (0 = no cap; default 100).")),
	)
	s.AddTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, args influencersListArgs) (*mcp.CallToolResult, error) {
		audit, closeAudit := openToolAudit(cfg, "influencers_list")
		defer closeAudit()
		limit := int(args.Limit)
		if limit == 0 {
			limit = 100
		}
		out, err := influencers.List(ctx, influencers.FilterOpts{
			Type:         args.Type,
			Topic:        args.Topic,
			HasPlatform:  args.Has,
			FollowedOnly: args.FollowedOnly,
			Limit:        limit,
		})
		if err != nil {
			audit.Logf("influencers_list FAILED: %v", err)
			return mcp.NewToolResultError(err.Error()), nil
		}
		audit.Logf("influencers_list returned %d entries", len(out))
		return jsonResult(map[string]any{
			"count":       len(out),
			"influencers": out,
		})
	}))
}

// ---- get -------------------------------------------------------------

type influencersGetArgs struct {
	Name string `json:"name"`
}

func addInfluencersGetTool(s *server.MCPServer, cfg Config) {
	tool := mcp.NewTool("social_ledger_influencers_get",
		mcp.WithDescription("Retrieve one tracked influencer by name or slug. Returns the full record (socials, topics, description, subscribed channels) or null when not tracked. Use mid-research: 'is the author of this article tracked, and what are they known for?' — answer informs how much weight to give the source. Slug derivation: lowercase + replace non-alnum with `-`, so 'Andrej Karpathy' → 'andrej-karpathy'."),
		mcp.WithString("name", mcp.Required(), mcp.Description("Display name ('Andrej Karpathy') or slug ('andrej-karpathy'). Both work; the lookup canonicalises to slug.")),
	)
	s.AddTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, args influencersGetArgs) (*mcp.CallToolResult, error) {
		audit, closeAudit := openToolAudit(cfg, "influencers_get")
		defer closeAudit()
		if strings.TrimSpace(args.Name) == "" {
			return mcp.NewToolResultError("name is required"), nil
		}
		out, err := influencers.Get(ctx, args.Name)
		if err != nil {
			audit.Logf("influencers_get FAILED: %v", err)
			return mcp.NewToolResultError(err.Error()), nil
		}
		if out == nil {
			audit.Logf("influencers_get %q: not tracked", args.Name)
			return jsonResult(map[string]any{
				"found":      false,
				"name":       args.Name,
				"influencer": nil,
			})
		}
		audit.Logf("influencers_get %q: found slug=%s", args.Name, out.Slug)
		return jsonResult(map[string]any{
			"found":      true,
			"influencer": out,
		})
	}))
}

// ---- add -------------------------------------------------------------

type influencersAddArgs struct {
	Name        string   `json:"name"`
	Type        string   `json:"type,omitempty"`
	Description string   `json:"description,omitempty"`
	Socials     []string `json:"socials,omitempty"`
	Topics      []string `json:"topics,omitempty"`
	Slug        string   `json:"slug,omitempty"`
}

func addInfluencersAddTool(s *server.MCPServer, cfg Config) {
	tool := mcp.NewTool("social_ledger_influencers_add",
		mcp.WithDescription("Add or upsert a tracked influencer. Re-running for the same name merges socials (new platform overwrites same key, others kept), unions topics, and replaces description when non-empty — so 'I just learned Jane's mastodon handle' is a one-line call without losing her existing linkedin/x. Use mid-research when you discover a new authority worth tracking ('the author of this Archon project, Cole Medin, ships a lot of agent content — track him for ai/agents'). Refused when SOCIAL_LEDGER_READONLY=1 is set."),
		mcp.WithString("name", mcp.Required(), mcp.Description("Display name as the user would write it ('Cole Medin', 'Vercel'). Slug is derived automatically (lowercase + dash) unless overridden via `slug`.")),
		mcp.WithString("type", mcp.Description("'person' (default) or 'company'.")),
		mcp.WithString("description", mcp.Description("Free-form description / bio. Replaces existing description on upsert when non-empty; pass empty string to leave it as-is.")),
		mcp.WithArray("socials", mcp.Description("Array of 'platform=handle' strings, e.g. ['linkedin=cole-medin-727752184', 'x=colemedin', 'github=coleam00', 'bluesky=@cole.bsky.social', 'website=https://example.com', 'mastodon=@cole@hachyderm.io']. Merged with existing entries on upsert (new wins for same platform key)."), mcp.Items(map[string]any{"type": "string"})),
		mcp.WithArray("topics", mcp.Description("Topics this influencer is an authority on, e.g. ['ai', 'agents', 'harness']. Union-merged with existing on upsert (sorted-dedup)."), mcp.Items(map[string]any{"type": "string"})),
		mcp.WithString("slug", mcp.Description("Override the canonical slug. Rare — only when two distinct people slugify to the same name and you need to disambiguate.")),
	)
	s.AddTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, args influencersAddArgs) (*mcp.CallToolResult, error) {
		audit, closeAudit := openToolAudit(cfg, "influencers_add")
		defer closeAudit()
		if ledger.ReadOnly() {
			return mcp.NewToolResultError(ledger.ErrReadOnly.Error()), nil
		}
		if strings.TrimSpace(args.Name) == "" {
			return mcp.NewToolResultError("name is required"), nil
		}
		socials, err := parseSocialPairs(args.Socials)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		out, err := influencers.Add(ctx, influencers.AddInput{
			Name:        args.Name,
			Slug:        args.Slug,
			Type:        args.Type,
			Description: args.Description,
			Socials:     socials,
			Topics:      args.Topics,
		})
		if err != nil {
			audit.Logf("influencers_add FAILED: %v", err)
			return mcp.NewToolResultError(err.Error()), nil
		}
		audit.Logf("influencers_add %q (%s) slug=%s", out.Name, out.Type, out.Slug)
		return jsonResult(map[string]any{
			"influencer": out,
		})
	}))
}

// parseSocialPairs turns ["linkedin=jane", "x=@jane"] into a map.
// Empty / whitespace-only entries are ignored. Bad entries (no `=`,
// empty key, empty value) produce a clear error so the agent fixes
// the call rather than silently dropping entries.
func parseSocialPairs(pairs []string) (map[string]string, error) {
	if len(pairs) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(pairs))
	for _, p := range pairs {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		eq := strings.Index(p, "=")
		if eq < 1 || eq == len(p)-1 {
			return nil, mcpInfluencerError("socials: expected platform=value, got " + p)
		}
		k := strings.ToLower(strings.TrimSpace(p[:eq]))
		v := strings.TrimSpace(p[eq+1:])
		if k == "" || v == "" {
			return nil, mcpInfluencerError("socials: empty key or value in " + p)
		}
		out[k] = v
	}
	return out, nil
}

// ---- remove ----------------------------------------------------------

type influencersRemoveArgs struct {
	Name string `json:"name"`
}

func addInfluencersRemoveTool(s *server.MCPServer, cfg Config) {
	tool := mcp.NewTool("social_ledger_influencers_remove",
		mcp.WithDescription("Delete a tracked influencer entirely (record + all subscribed channels). Idempotent — calling on a name that's not tracked returns {removed: false} rather than an error. Refused when SOCIAL_LEDGER_READONLY=1 is set. Use sparingly — for 'I unsubscribed from this channel' prefer social_ledger_influencers_unsubscribe; this tool is for 'I no longer want to track this person at all'."),
		mcp.WithString("name", mcp.Required(), mcp.Description("Display name or slug.")),
	)
	s.AddTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, args influencersRemoveArgs) (*mcp.CallToolResult, error) {
		audit, closeAudit := openToolAudit(cfg, "influencers_remove")
		defer closeAudit()
		if ledger.ReadOnly() {
			return mcp.NewToolResultError(ledger.ErrReadOnly.Error()), nil
		}
		if strings.TrimSpace(args.Name) == "" {
			return mcp.NewToolResultError("name is required"), nil
		}
		removed, err := influencers.Remove(ctx, args.Name)
		if err != nil {
			audit.Logf("influencers_remove FAILED: %v", err)
			return mcp.NewToolResultError(err.Error()), nil
		}
		audit.Logf("influencers_remove %q: removed=%v", args.Name, removed)
		return jsonResult(map[string]any{
			"name":    args.Name,
			"removed": removed,
		})
	}))
}

// ---- subscribe -------------------------------------------------------

type influencersSubscribeArgs struct {
	Name     string   `json:"name"`
	Platform string   `json:"platform"`
	Topics   []string `json:"topics,omitempty"`
	Note     string   `json:"note,omitempty"`
}

func addInfluencersSubscribeTool(s *server.MCPServer, cfg Config) {
	tool := mcp.NewTool("social_ledger_influencers_subscribe",
		mcp.WithDescription("Mark a specific channel of a tracked influencer as 'subscribed' — the agent should refresh this feed when researching the listed topics. Upserts: re-subscribing on the same platform unions the topics list and replaces the note when non-empty. The influencer must already be added via social_ledger_influencers_add first. Use to express research intent ('subscribe to Karpathy's X for AI specifically — not interested in his bluesky right now'). Refused when SOCIAL_LEDGER_READONLY=1 is set."),
		mcp.WithString("name", mcp.Required(), mcp.Description("Display name or slug of an already-tracked influencer.")),
		mcp.WithString("platform", mcp.Required(), mcp.Description("Channel to subscribe to: 'x', 'linkedin', 'github', 'bluesky', 'mastodon', 'website', etc. Should match a key in the influencer's socials map (otherwise the subscription points at a channel we don't have a handle for, which is allowed but useless until a handle is added).")),
		mcp.WithArray("topics", mcp.Description("Optional scope: only refresh this channel for these topics. Empty = subscribe for everything the influencer is known for. E.g. ['ai', 'research']."), mcp.Items(map[string]any{"type": "string"})),
		mcp.WithString("note", mcp.Description("Optional free-form note explaining why ('main feed for transformer research'). Replaces existing note on re-subscribe when non-empty.")),
	)
	s.AddTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, args influencersSubscribeArgs) (*mcp.CallToolResult, error) {
		audit, closeAudit := openToolAudit(cfg, "influencers_subscribe")
		defer closeAudit()
		if ledger.ReadOnly() {
			return mcp.NewToolResultError(ledger.ErrReadOnly.Error()), nil
		}
		if strings.TrimSpace(args.Name) == "" {
			return mcp.NewToolResultError("name is required"), nil
		}
		if strings.TrimSpace(args.Platform) == "" {
			return mcp.NewToolResultError("platform is required"), nil
		}
		out, err := influencers.Subscribe(ctx, influencers.FollowInput{
			NameOrSlug: args.Name,
			Platform:   args.Platform,
			Topics:     args.Topics,
			Note:       args.Note,
		})
		if err != nil {
			audit.Logf("influencers_subscribe FAILED: %v", err)
			return mcp.NewToolResultError(err.Error()), nil
		}
		audit.Logf("influencers_subscribe %q on %s", out.Name, args.Platform)
		return jsonResult(map[string]any{
			"influencer": out,
		})
	}))
}

// ---- unsubscribe -----------------------------------------------------

type influencersUnsubscribeArgs struct {
	Name     string `json:"name"`
	Platform string `json:"platform"`
}

func addInfluencersUnsubscribeTool(s *server.MCPServer, cfg Config) {
	tool := mcp.NewTool("social_ledger_influencers_unsubscribe",
		mcp.WithDescription("Stop tracking a specific channel of an influencer. Idempotent — calling for a platform that isn't subscribed returns {removed: false}. Does NOT delete the influencer record itself; their other channels and topics stay intact. Refused when SOCIAL_LEDGER_READONLY=1 is set."),
		mcp.WithString("name", mcp.Required(), mcp.Description("Display name or slug.")),
		mcp.WithString("platform", mcp.Required(), mcp.Description("Channel to unsubscribe from.")),
	)
	s.AddTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, args influencersUnsubscribeArgs) (*mcp.CallToolResult, error) {
		audit, closeAudit := openToolAudit(cfg, "influencers_unsubscribe")
		defer closeAudit()
		if ledger.ReadOnly() {
			return mcp.NewToolResultError(ledger.ErrReadOnly.Error()), nil
		}
		if strings.TrimSpace(args.Name) == "" {
			return mcp.NewToolResultError("name is required"), nil
		}
		if strings.TrimSpace(args.Platform) == "" {
			return mcp.NewToolResultError("platform is required"), nil
		}
		inf, removed, err := influencers.Unsubscribe(ctx, args.Name, args.Platform)
		if err != nil {
			audit.Logf("influencers_unsubscribe FAILED: %v", err)
			return mcp.NewToolResultError(err.Error()), nil
		}
		audit.Logf("influencers_unsubscribe %q from %s: removed=%v", args.Name, args.Platform, removed)
		return jsonResult(map[string]any{
			"removed":    removed,
			"platform":   args.Platform,
			"influencer": inf,
		})
	}))
}

// mcpInfluencerError is a typed error for argument validation —
// kept tiny since these messages are user-facing on the agent side.
type mcpInfluencerError string

func (e mcpInfluencerError) Error() string { return string(e) }
