package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/jedi4ever/social-skills/internal/core"
	"github.com/jedi4ever/social-skills/internal/ledger"
	"github.com/jedi4ever/social-skills/internal/platforms/linkedin"
	"github.com/jedi4ever/social-skills/internal/platforms/twitter"
	"github.com/jedi4ever/social-skills/internal/render"
)

// timelineFlags is parsed from `social-fetch timeline` args.
type timelineFlags struct {
	provider      string // explicit -p; auto-detected from URL when empty
	kind          string // all | posts | comments | reactions | tweets | replies | retweets
	max           int
	format        string
	output        string
	logFile       string
	before        *time.Time
	after         *time.Time
	timeout       time.Duration
	expand        bool
	excludeShares bool
	user          string
}

func parseTimelineFlags(args []string) (*timelineFlags, error) {
	f := &timelineFlags{
		kind:    "all",
		max:     30,
		format:  "markdown",
		timeout: 60 * time.Second,
	}
	var positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch a {
		case "-h", "--help":
			printTimelineHelp(os.Stdout)
			os.Exit(0)
		case "-p", "--provider":
			i++
			if i >= len(args) {
				return nil, fmt.Errorf("--provider needs a value")
			}
			f.provider = args[i]
		case "--kind":
			i++
			if i >= len(args) {
				return nil, fmt.Errorf("--kind needs a value")
			}
			f.kind = args[i]
		case "-n", "--max":
			i++
			if i >= len(args) {
				return nil, fmt.Errorf("--max needs a value")
			}
			n, err := atoi(args[i])
			if err != nil {
				return nil, err
			}
			f.max = n
		case "-f", "--format":
			i++
			if i >= len(args) {
				return nil, fmt.Errorf("--format needs a value")
			}
			f.format = args[i]
		case "-o", "--output":
			i++
			if i >= len(args) {
				return nil, fmt.Errorf("--output needs a value")
			}
			f.output = args[i]
		case "-l", "--log":
			i++
			if i >= len(args) {
				return nil, fmt.Errorf("--log needs a value")
			}
			f.logFile = args[i]
		case "--after":
			i++
			if i >= len(args) {
				return nil, fmt.Errorf("--after needs a value")
			}
			t, err := parseDateFlag(args[i])
			if err != nil {
				return nil, fmt.Errorf("--after: %w", err)
			}
			f.after = &t
		case "--before":
			i++
			if i >= len(args) {
				return nil, fmt.Errorf("--before needs a value")
			}
			t, err := parseDateFlag(args[i])
			if err != nil {
				return nil, fmt.Errorf("--before: %w", err)
			}
			f.before = &t
		case "--last":
			i++
			if i >= len(args) {
				return nil, fmt.Errorf("--last needs a value")
			}
			d, err := time.ParseDuration(args[i])
			if err != nil {
				if dd, derr := parseDaysDuration(args[i]); derr == nil {
					d = dd
				} else {
					return nil, fmt.Errorf("--last: %w", err)
				}
			}
			t := time.Now().Add(-d)
			f.after = &t
		case "--expand":
			f.expand = true
		case "--no-reshares", "--no-reposts":
			f.excludeShares = true
		case "--reshares":
			f.excludeShares = false
		case "--timeout":
			i++
			if i >= len(args) {
				return nil, fmt.Errorf("--timeout needs a value")
			}
			d, err := time.ParseDuration(args[i])
			if err != nil {
				return nil, fmt.Errorf("--timeout: %w", err)
			}
			f.timeout = d
		default:
			if strings.HasPrefix(a, "-") {
				return nil, fmt.Errorf("unknown flag %q", a)
			}
			positional = append(positional, a)
		}
	}
	if len(positional) == 0 {
		return nil, errors.New("no user given (pass a handle, @handle, or profile URL)")
	}
	if len(positional) > 1 {
		return nil, fmt.Errorf("expected one user, got %d", len(positional))
	}
	f.user = positional[0]
	return f, nil
}

func runTimeline(args []string) error {
	flags, err := parseTimelineFlags(args)
	if err != nil {
		return err
	}

	provider, user, err := core.ParseIdentifier(flags.user, flags.provider)
	if err != nil {
		return err
	}

	format, err := render.ParseFormat(flags.format)
	if err != nil {
		return err
	}

	audit, closeAudit, err := openAudit("timeline", flags.logFile)
	if err != nil {
		return err
	}
	defer closeAudit()

	reg := core.NewTimelineRegistry(
		twitter.NewXProvider(twitter.NewSearchProvider()),
		linkedin.NewLinkedInProvider(),
	)
	p, err := reg.Get(provider)
	if err != nil {
		return err
	}

	ctx, cancel := signalContext(flags.timeout)
	ctx = core.WithAudit(ctx, audit)
	defer cancel()

	audit.Logf("timeline %s/%s (kind=%s, max=%d)", provider, user, flags.kind, flags.max)
	item, err := p.Fetch(ctx, user, core.TimelineOptions{
		Kind:          flags.kind,
		Max:           flags.max,
		After:         flags.after,
		Before:        flags.before,
		Expand:        flags.expand,
		ExcludeShares: flags.excludeShares,
		Audit:         audit,
	})
	if err != nil {
		audit.Logf("timeline FAILED: %v", err)
		return err
	}
	audit.Logf("timeline returned %d items", len(item.Children))

	out, closeOut, err := openOutput(flags.output)
	if err != nil {
		return err
	}
	defer closeOut()
	if err := render.Item(out, item, format); err != nil {
		return err
	}
	// Auto-ingest the timeline parent + each child post into the
	// ledger when SOCIALFETCH_LEDGER=1. Children are individually
	// addressable items (each has its own URL), so the ledger
	// indexes them as separate entries — matches how a per-URL
	// fetch of one of those posts would land.
	if item != nil {
		toIngest := []core.Item{*item}
		for _, child := range item.Children {
			toIngest = append(toIngest, child)
		}
		ledger.Ingest(ctx, toIngest...)
	}
	return nil
}

func printTimelineHelp(w io.Writer) {
	fmt.Fprint(w, `social-fetch timeline — fetch a user's recent activity

Usage:
  social-fetch timeline <user-or-url> [flags]

User identifier accepts:
  swyx                                    bare handle (provider defaults to x)
  @swyx                                   '@' implies x
  https://x.com/swyx                      x (auto-detected)
  patrickdebois        + -p linkedin      bare handle, LinkedIn
  https://www.linkedin.com/in/swyx-io/    linkedin (auto-detected)

Flags:
  -p, --provider  NAME   x (default for bare handles) | linkedin
      --kind      KIND   x:        all (default), tweets, replies, retweets
                         linkedin: all (default), posts, comments, reactions
  -n, --max       N      max items (default 30)
      --last      DUR    sugar for --after (e.g. 7d, 24h, 1m)
                         x has a hard 7-day cap
      --after     DATE   yyyy-mm-dd or RFC3339
      --before    DATE   yyyy-mm-dd or RFC3339
      --expand           (LinkedIn) re-fetch each item via the post fetcher
      --no-reshares      (LinkedIn) drop reposts/reshares from the timeline
                         (default includes them, tagged kind=repost)
  -f, --format    FMT    markdown (default), json, jsonl
  -o, --output    PATH   "-" or unset = stdout, FILE = single file
  -l, --log       PATH   audit/debug log destination
      --timeout   DUR    overall timeout (default 60s)
  -h, --help

Auth:
  x          requires X_API_KEY + X_API_SECRET
  linkedin   requires the bridge — run 'social-fetch bridge start' first

Examples:
  social-fetch timeline swyx --last 7d
  social-fetch timeline @swyx --kind tweets -n 50
  social-fetch timeline patrickdebois -p linkedin --kind posts
  social-fetch timeline https://www.linkedin.com/in/swyx-io/ --kind reactions

Notes:
  - LinkedIn timelines are first-page only (typically 5-20 items).
    Scroll-to-load is a follow-up.
  - X timeline is search-backed; no retweets unless --kind retweets.
`)
}
