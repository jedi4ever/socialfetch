// Package notifier defines the provider abstraction for sending
// short notifications out of a research session — Slack today,
// Discord / email / webhook / PagerDuty later. Mirrors how
// internal/core's Fetcher / SearchProvider abstract per-platform
// fetch+search differences in social-fetch: each provider
// implements one interface and registers itself; the CLI + MCP
// surfaces stay provider-agnostic.
//
// The contract is intentionally narrow: post a message to a
// channel, get back a stable id + permalink. Read flows, threading
// helpers, reactions, etc. are out of scope for v1 — extending
// the interface later (or layering a richer one) doesn't break
// callers wanting just the post path.
package notifier

import "context"

// Provider is the per-channel-platform implementation. Stateless
// after construction — keep all auth (tokens, signing keys) in
// the constructed instance, not in PostOpts.
type Provider interface {
	// Name is the short identifier the CLI + MCP surface use to
	// route ("slack", "discord", …). Lowercase, no spaces.
	Name() string

	// Post delivers msg to the supplied channel and returns the
	// platform's record id + permalink. Implementations surface
	// upstream errors directly — callers expect to see Slack's
	// "channel_not_found" / Discord's rate-limit / etc. without
	// us papering over them.
	Post(ctx context.Context, opts PostOpts) (*Result, error)
}

// PostOpts carries everything a single notification needs. Channel
// is provider-scoped (Slack channel id "C123ABC" or name
// "#research"; Discord channel snowflake; …). Text is the plain
// fallback rendering — every provider must accept it. Blocks is
// the optional structured payload (Slack's blocks array, Discord
// embeds, etc.) as raw JSON the caller already serialised; pass
// nil when only Text is needed.
//
// ThreadID lets the caller reply to an earlier post (Slack ts,
// Discord parent message id) so a long-running run's status
// updates can collapse into one thread instead of spamming the
// channel. Empty = top-level post.
type PostOpts struct {
	Channel  string
	Text     string
	Blocks   []byte
	ThreadID string
}

// Result is what the caller gets back: the platform-side message
// id (Slack ts, Discord snowflake) and a permalink the operator
// can click. ID is the value to pass back as PostOpts.ThreadID
// for follow-up replies.
type Result struct {
	ID        string `json:"id"`
	Permalink string `json:"permalink,omitempty"`
}
