package main

// cmd_post.go — `social-notifier post` CLI. Builds a PostOpts
// from flags, looks up the provider in the registry, calls Post.
// JSON output (one line) so callers can pipe into jq for the
// returned message id / permalink.

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/jedi4ever/social-skills/internal/notifier"
)

func cmdPost(args []string) error {
	fs := flag.NewFlagSet("post", flag.ContinueOnError)
	provider := fs.String("provider", "", "provider name (default: first registered, today: slack). See `social-notifier providers list`.")
	channel := fs.String("channel", "", "channel id or name (provider-scoped). Falls back to provider default (e.g. SLACK_DEFAULT_CHANNEL).")
	jsonBlocks := fs.String("json", "", "structured payload as raw JSON — Slack blocks array, Discord embeds, etc. Provider-specific shape.")
	thread := fs.String("thread", "", "reply to this earlier message id (Slack ts, Discord parent id). Empty = top-level post.")
	if err := fs.Parse(args); err != nil {
		return err
	}

	// Remaining positional args are the message text. Multiple
	// args get joined with spaces — `social-notifier post hello world`
	// posts "hello world" without quoting.
	text := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if text == "" && *jsonBlocks == "" {
		return fmt.Errorf("post: message text or --json blocks required")
	}

	prov, err := notifier.Get(*provider)
	if err != nil {
		return err
	}

	opts := notifier.PostOpts{
		Channel:  *channel,
		Text:     text,
		ThreadID: *thread,
	}
	if *jsonBlocks != "" {
		// Validate it's at least syntactically JSON before
		// shipping it across the network — saves a round-trip
		// when the operator typo'd a brace.
		var probe any
		if err := json.Unmarshal([]byte(*jsonBlocks), &probe); err != nil {
			return fmt.Errorf("--json: invalid JSON: %w", err)
		}
		opts.Blocks = []byte(*jsonBlocks)
	}

	res, err := prov.Post(context.Background(), opts)
	if err != nil {
		return err
	}
	body, _ := json.Marshal(res)
	fmt.Fprintln(os.Stdout, string(body))
	return nil
}
