// Package slack is the Slack provider for the notifier surface —
// posts to chat.postMessage with a Bot Token (xoxb-…). One
// upstream call per Post; no batching, no rate-limit retry yet
// (Slack's per-bot ceiling is generous for status-update use,
// and a tight inner-claude loop hitting it is a misuse worth
// surfacing as an error).
//
// Bot Token is read from SLACK_BOT_TOKEN at construction time.
// Optional channel default lives in SLACK_DEFAULT_CHANNEL so
// `social-notifier post "msg"` (no --channel) works against the
// operator's chosen channel.
package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/jedi4ever/social-skills/internal/notifier"
)

// Provider is the Slack implementation. Holds the bot token and
// a configurable HTTP client so tests can swap a recorder.
type Provider struct {
	BotToken       string
	DefaultChannel string
	HTTP           *http.Client
}

// Name implements notifier.Provider.
func (p *Provider) Name() string { return "slack" }

// New constructs a Slack provider from env. Returns an error when
// SLACK_BOT_TOKEN is missing — every other provider knob is
// optional. Caller can also build the struct directly when
// they've got the token from somewhere other than env (e.g. a
// test).
func New() (*Provider, error) {
	tok := strings.TrimSpace(os.Getenv("SLACK_BOT_TOKEN"))
	if tok == "" {
		return nil, fmt.Errorf("SLACK_BOT_TOKEN is required for the slack provider (set it in .env or your shell)")
	}
	return &Provider{
		BotToken:       tok,
		DefaultChannel: strings.TrimSpace(os.Getenv("SLACK_DEFAULT_CHANNEL")),
		HTTP:           &http.Client{Timeout: 30 * time.Second},
	}, nil
}

// Post hits chat.postMessage. Returns the message ts (Slack's
// canonical id) and a permalink the operator can click. When
// PostOpts.Blocks is non-empty it's passed through verbatim — we
// don't validate the structured shape; let Slack's API surface
// the error if it's malformed.
func (p *Provider) Post(ctx context.Context, opts notifier.PostOpts) (*notifier.Result, error) {
	channel := opts.Channel
	if channel == "" {
		channel = p.DefaultChannel
	}
	if channel == "" {
		return nil, fmt.Errorf("slack: channel required (pass --channel or set SLACK_DEFAULT_CHANNEL)")
	}
	if strings.TrimSpace(opts.Text) == "" && len(opts.Blocks) == 0 {
		return nil, fmt.Errorf("slack: text or blocks required")
	}

	// Build chat.postMessage body. Slack accepts either text or
	// blocks; we send both when both are supplied (text becomes
	// the notification fallback for clients that don't render
	// blocks — phone notifications, screen readers).
	body := map[string]any{
		"channel": channel,
	}
	if opts.Text != "" {
		body["text"] = opts.Text
	}
	if len(opts.Blocks) > 0 {
		// Blocks is raw JSON the caller already serialised. Pass
		// through as json.RawMessage so the server sees the
		// original bytes, not a re-encoding.
		body["blocks"] = json.RawMessage(opts.Blocks)
	}
	if opts.ThreadID != "" {
		body["thread_ts"] = opts.ThreadID
	}

	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("slack: marshal body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://slack.com/api/chat.postMessage", bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("slack: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Authorization", "Bearer "+p.BotToken)

	resp, err := p.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("slack: chat.postMessage: %w", err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("slack: read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("slack: chat.postMessage HTTP %d: %s", resp.StatusCode, snippet(respBytes))
	}
	// Slack returns 200 even on logical errors; the body's `ok`
	// field is the dispositive signal.
	var out struct {
		OK      bool   `json:"ok"`
		Error   string `json:"error"`
		TS      string `json:"ts"`
		Channel string `json:"channel"`
		// chat.postMessage doesn't return a permalink, but we can
		// derive one from chat.getPermalink — skip for v1 and let
		// callers compute it themselves if they want a click-able
		// link. ID is what they really need for threading.
	}
	if err := json.Unmarshal(respBytes, &out); err != nil {
		return nil, fmt.Errorf("slack: decode response: %w (body: %s)", err, snippet(respBytes))
	}
	if !out.OK {
		return nil, fmt.Errorf("slack: chat.postMessage error: %s", out.Error)
	}
	return &notifier.Result{ID: out.TS}, nil
}

// snippet trims a response body to a short single-line preview
// for error messages. Long Slack errors (HTML when something is
// very wrong) blow up logs without this.
func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > 240 {
		s = s[:237] + "..."
	}
	return s
}

func init() {
	// Register a deferred-construction wrapper so the provider
	// shows up in `social-notifier providers list` even when
	// SLACK_BOT_TOKEN isn't set — only `Post` requires the token.
	// Get-by-name wraps construction; the CLI surfaces the auth
	// error at post-time, which is the natural place.
	notifier.Register(stub{})
}

// stub is the registration shim. Real construction happens lazily
// in Post — this lets `providers list` enumerate without env vars
// being set. Two concerns split: registration (always) vs.
// construction (when used).
type stub struct{}

func (stub) Name() string { return "slack" }
func (s stub) Post(ctx context.Context, opts notifier.PostOpts) (*notifier.Result, error) {
	p, err := New()
	if err != nil {
		return nil, err
	}
	return p.Post(ctx, opts)
}
