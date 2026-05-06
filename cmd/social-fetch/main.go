// Command social-fetch is a CLI for fetching social-media URLs (HackerNews,
// Reddit, GitHub, Twitter, Medium/Substack/articles, RSS) and search
// queries (DuckDuckGo, SerpAPI), and rendering the result as JSON, JSONL,
// or markdown.
//
// Subcommands:
//
//	social-fetch fetch <url> [<url>...]      fetch one or more URLs
//	social-fetch search "<query>"            run a search query
//	social-fetch list                        list fetchers and search providers
//	social-fetch help [sub]                  show help
//
// Run `social-fetch help` for the full reference.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/jedi4ever/social-skills/internal/bridge"
	"github.com/jedi4ever/social-skills/internal/core"
	"github.com/jedi4ever/social-skills/internal/ledger"
	"github.com/jedi4ever/social-skills/internal/render"
	"github.com/jedi4ever/social-skills/internal/util/dotenv"

	"github.com/jedi4ever/social-skills/internal/platforms/anthropic"
	"github.com/jedi4ever/social-skills/internal/platforms/article"
	"github.com/jedi4ever/social-skills/internal/platforms/arxiv"
	"github.com/jedi4ever/social-skills/internal/platforms/bluesky"
	"github.com/jedi4ever/social-skills/internal/platforms/brave"
	"github.com/jedi4ever/social-skills/internal/platforms/duckduckgo"
	"github.com/jedi4ever/social-skills/internal/platforms/github"
	"github.com/jedi4ever/social-skills/internal/platforms/google"
	"github.com/jedi4ever/social-skills/internal/platforms/grok"
	"github.com/jedi4ever/social-skills/internal/platforms/hackernews"
	"github.com/jedi4ever/social-skills/internal/platforms/linkedin"
	"github.com/jedi4ever/social-skills/internal/platforms/medium"
	"github.com/jedi4ever/social-skills/internal/platforms/openai"
	"github.com/jedi4ever/social-skills/internal/platforms/perplexity"
	"github.com/jedi4ever/social-skills/internal/platforms/reddit"
	"github.com/jedi4ever/social-skills/internal/platforms/rss"
	"github.com/jedi4ever/social-skills/internal/platforms/serpapi"
	"github.com/jedi4ever/social-skills/internal/platforms/substack"
	"github.com/jedi4ever/social-skills/internal/platforms/tavily"
	"github.com/jedi4ever/social-skills/internal/platforms/twitter"
	"github.com/jedi4ever/social-skills/internal/platforms/youtube"
)

// Version is the user-visible social-fetch version. Bump this on every
// user-visible release. See CLAUDE.md "Versioning" for the rule.
const Version = "0.16.4"

// defaultAskChain is the fallback order used by `-p auto`. Cheap +
// reliable first (perplexity has the highest hit rate on grounded
// questions in our experience), graceful degradation through the
// rest. SerpAPI and Tavily go last because their answers tend to be
// shallower than the LLM-grounded providers.
var defaultAskChain = []string{
	"perplexity", "grok", "openai", "anthropic", "gemini", "tavily", "serpapi",
}

// defaultSearchChain is the fallback order used by `-p auto` for the
// search subcommand. Perplexity first (the index that powers Sonar —
// strongest signal for AI/news/research queries), Tavily second (also
// AI-tuned), Brave third (paid but no quota cliff), SerpAPI fourth
// (Google-results proxy, cheap free tier), DuckDuckGo last (no auth,
// quality varies).
var defaultSearchChain = []string{
	"perplexity", "tavily", "brave", "serpapi", "duckduckgo",
}

// resolveAsker picks an Asker from the user-supplied -p / --fallback
// expression:
//
//   - "perplexity"        → that single provider
//   - "auto"              → defaultAskChain (in order, falling through)
//   - "perplexity,grok"   → comma-list interpreted as a custom chain
//
// A single-provider name still goes through the chain machinery when
// it contains a comma, so behaviour is consistent. Single-name
// "auto"-but-no-comma doesn't allocate a chain.
func resolveAsker(expr string) (core.Asker, error) {
	reg := buildAskers()
	expr = strings.TrimSpace(expr)
	if strings.EqualFold(expr, "auto") {
		return core.NewAskChain(reg, defaultAskChain)
	}
	if strings.Contains(expr, ",") {
		parts := splitAndTrim(expr, ",")
		return core.NewAskChain(reg, parts)
	}
	return reg.Get(expr)
}

// resolveSearcher mirrors resolveAsker for the search subcommand.
func resolveSearcher(expr string) (core.SearchProvider, error) {
	_, reg := buildRegistries()
	expr = strings.TrimSpace(expr)
	if strings.EqualFold(expr, "auto") {
		return core.NewSearchChain(reg, defaultSearchChain)
	}
	if strings.Contains(expr, ",") {
		parts := splitAndTrim(expr, ",")
		return core.NewSearchChain(reg, parts)
	}
	return reg.Get(expr)
}

func splitAndTrim(s, sep string) []string {
	raw := strings.Split(s, sep)
	out := make([]string, 0, len(raw))
	for _, p := range raw {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// buildAskers returns the registry of "answer engines" used by the
// `ask` subcommand. Kept separate from search because the conceptual
// shape (synthesized answer + sources) differs from a flat result list.
func buildAskers() *core.AskRegistry {
	return core.NewAskRegistry(
		perplexity.New(),
		grok.New(),
		openai.New(),
		anthropic.New(),
		google.NewAsker(),
		tavily.NewAsker(),
		serpapi.NewAsker(),
	)
}

// buildRegistries wires up the default fetcher and search registries.
// The fetcher order matters: specific sources first, generic article last.
func buildRegistries() (*core.Registry, *core.SearchRegistry) {
	fetchers := core.NewRegistry(
		hackernews.New(),
		reddit.New(),
		github.New(),
		twitter.New(),
		linkedin.New(),
		youtube.New(),
		bluesky.New(),
		arxiv.New(),
		// Medium and Substack come BEFORE the article fallback so they
		// route through the bridge (paywall-aware) instead of plain
		// HTTP. Each falls back to direct HTTP automatically when the
		// bridge isn't running.
		medium.New(),
		substack.New(),
		rss.New(),
		article.New(), // catch-all — must be last
	)
	searchers := core.NewSearchRegistry(
		duckduckgo.New(),
		google.New(),
		brave.New(),
		serpapi.New(),
		serpapi.NewNewsProvider(),
		tavily.New(),
		perplexity.NewSearchProvider(),
		hackernews.NewSearchProvider(),
		reddit.NewSearchProvider(),
		twitter.NewSearchProvider(),
		youtube.NewSearchProvider(),
		bluesky.NewSearchProvider(),
		arxiv.NewSearchProvider(),
		linkedin.NewSearchProvider(),
	)
	return fetchers, searchers
}

func main() {
	loadDotEnv()
	start := time.Now()
	cmd := ""
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	err := run(os.Args[1:])
	emitExitAudit(cmd, start, err)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// emitExitAudit appends a single "exit ..." line to the global audit
// log so a `monitor` watcher can see when an invocation finished and
// whether it succeeded. Skipped for the monitor subcommand itself
// (it never naturally exits — Ctrl-C kills it — and we'd be writing
// to the same file we're tailing).
func emitExitAudit(cmd string, start time.Time, runErr error) {
	if cmd == "monitor" || cmd == "" {
		return
	}
	globalW, closeFn, err := core.OpenGlobalAudit(cmd)
	if err != nil || globalW == nil {
		return
	}
	defer closeFn()
	audit := core.NewAuditLogger(nil)
	audit.AttachGlobal(globalW)
	dur := time.Since(start).Round(time.Millisecond)
	if runErr != nil {
		audit.Logf("exit %s code=1 in %s: %v", cmd, dur, runErr)
		return
	}
	audit.Logf("exit %s code=0 in %s", cmd, dur)
}

// loadDotEnv pulls KEY=VALUE pairs from .env into the process env via
// dotenv.LoadAuto, which walks up from cwd and from the binary's
// install dir. See dotenv.LoadAuto for the resolver semantics — same
// helper is used by every live test in the repo so the discovery
// rules stay consistent across CLI runtime and `go test`.
func loadDotEnv() {
	dotenv.LoadAuto()
}

func run(args []string) error {
	if len(args) == 0 {
		printUsage(os.Stdout)
		return nil
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "fetch":
		return runFetch(rest)
	case "screenshot":
		return runScreenshot(rest)
	case "search":
		return runSearch(rest)
	case "timeline":
		return runTimeline(rest)
	case "ask":
		return runAsk(rest)
	case "research":
		return runResearch(rest)
	case "bridge":
		return runBridge(rest)
	case "bookmarks":
		return runBookmarks(rest)
	case "monitor":
		return runMonitor(rest)
	case "mcp":
		return runMCP(rest)
	case "list":
		return runList()
	case "hints":
		return runHints(rest)
	case "help", "-h", "--help":
		return runHelp(rest)
	case "version", "--version":
		fmt.Println("social-fetch " + Version)
		return nil
	default:
		printUsage(os.Stderr)
		return fmt.Errorf("unknown command %q", cmd)
	}
}

// fetchFlags is parsed from the args after `social-fetch fetch`.
type fetchFlags struct {
	format         string
	output         string // "" = stdout, file, or dir/
	inputFile      string // file with one URL per line
	logFile        string // audit/debug log destination ("-" = stderr)
	comments       bool
	maxComment     int
	jobs           int // worker-pool size for batch fetches
	genericExtract bool
	timeout        time.Duration
	urls           []string
}

func parseFetchFlags(args []string) (*fetchFlags, error) {
	f := &fetchFlags{
		format:   "markdown",
		comments: true,
		jobs:     4,
		timeout:  60 * time.Second,
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch a {
		case "-h", "--help":
			printFetchHelp(os.Stdout)
			os.Exit(0)
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
		case "-i", "--input":
			i++
			if i >= len(args) {
				return nil, fmt.Errorf("--input needs a value")
			}
			f.inputFile = args[i]
		case "-l", "--log":
			i++
			if i >= len(args) {
				return nil, fmt.Errorf("--log needs a value")
			}
			f.logFile = args[i]
		case "--no-comments":
			f.comments = false
		case "--comments":
			f.comments = true
		case "--generic-extraction":
			f.genericExtract = true
		case "--max-comments":
			i++
			if i >= len(args) {
				return nil, fmt.Errorf("--max-comments needs a value")
			}
			n, err := atoi(args[i])
			if err != nil {
				return nil, err
			}
			f.maxComment = n
		case "-j", "--jobs":
			i++
			if i >= len(args) {
				return nil, fmt.Errorf("--jobs needs a value")
			}
			n, err := atoi(args[i])
			if err != nil {
				return nil, err
			}
			if n < 1 {
				n = 1
			}
			f.jobs = n
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
			f.urls = append(f.urls, a)
		}
	}
	return f, nil
}

func runFetch(args []string) error {
	flags, err := parseFetchFlags(args)
	if err != nil {
		return err
	}

	format, err := render.ParseFormat(flags.format)
	if err != nil {
		return err
	}

	urls, err := collectURLs(flags.urls, flags.inputFile)
	if err != nil {
		return err
	}
	if len(urls) == 0 {
		printFetchHelp(os.Stderr)
		return errors.New("no URLs given (pass them as arguments or via --input FILE)")
	}

	audit, closeAudit, err := openAudit("fetch", flags.logFile)
	if err != nil {
		return err
	}
	defer closeAudit()

	opts := core.Options{
		IncludeComments:   flags.comments,
		MaxComments:       flags.maxComment,
		GenericExtraction: flags.genericExtract,
		Audit:             audit,
	}

	registry, _ := buildRegistries()
	ctx, cancel := signalContext(flags.timeout)
	ctx = core.WithAudit(ctx, audit)
	defer cancel()

	// Decide where output goes. For stdout / single file: stream as we go.
	// For a directory: write one file per URL.
	if isDirOutput(flags.output) {
		return fetchToDir(ctx, registry, urls, opts, format, flags.output, flags.jobs)
	}

	out, closeOut, err := openOutput(flags.output)
	if err != nil {
		return err
	}
	defer closeOut()

	// JSONL is the only sensible default for batches when streaming.
	if len(urls) > 1 && format == render.FormatJSON {
		format = render.FormatJSONL
		opts.Audit.Logf("multiple URLs with json format; emitting jsonl")
	}

	return fetchStreamOrdered(ctx, registry, urls, opts, format, out, flags.jobs)
}

// fetchStreamOrdered runs fetches concurrently with `jobs` workers but
// emits output in the original URL order — readers don't have to deal
// with interleaved or out-of-order results. Each result is emitted to w
// as soon as its slot in the input order is ready, so streaming consumers
// see early items without waiting for slow ones at the end.
func fetchStreamOrdered(ctx context.Context, reg *core.Registry, urls []string, opts core.Options, format render.Format, w io.Writer, jobs int) error {
	if jobs < 1 {
		jobs = 1
	}
	type result struct {
		item *core.Item
		err  error
	}
	results := make([]chan result, len(urls))
	for i := range results {
		results[i] = make(chan result, 1)
	}

	sem := make(chan struct{}, jobs)
	for i, u := range urls {
		sem <- struct{}{}
		go func(i int, u string) {
			defer func() { <-sem }()
			item, err := reg.Fetch(ctx, u, opts)
			results[i] <- result{item: item, err: err}
		}(i, u)
	}

	var firstErr error
	var ingestQueue []core.Item
	for i, u := range urls {
		r := <-results[i]
		if r.err != nil {
			fmt.Fprintf(os.Stderr, "fetch %s: %v\n", u, r.err)
			if firstErr == nil {
				firstErr = r.err
			}
			continue
		}
		if err := render.Item(w, r.item, format); err != nil {
			return err
		}
		if r.item != nil {
			ingestQueue = append(ingestQueue, *r.item)
		}
		if format == render.FormatMarkdown && len(urls) > 1 && i < len(urls)-1 {
			fmt.Fprint(w, "\n---\n\n")
		}
	}
	// Auto-ledger: when SOCIAL_LEDGER=1 is set, hand the
	// successful items to social-ledger via subprocess so the
	// agent doesn't have to wire up the JSONL pipe by hand. No-op
	// + nil error when the env var isn't set or the binary is
	// unavailable — see internal/ledger for the failure semantics.
	ledger.Ingest(ctx, ingestQueue...)
	return firstErr
}

// fetchToDir writes one output file per URL into dir, running up to
// `jobs` fetches concurrently.
func fetchToDir(ctx context.Context, reg *core.Registry, urls []string, opts core.Options, format render.Format, dir string, jobs int) error {
	if jobs < 1 {
		jobs = 1
	}
	if err := os.MkdirAll(strings.TrimRight(dir, "/"), 0o755); err != nil {
		return err
	}
	ext := extFor(format)

	var wg sync.WaitGroup
	errCh := make(chan error, len(urls))
	sem := make(chan struct{}, jobs)
	itemCh := make(chan core.Item, len(urls))

	for _, u := range urls {
		wg.Add(1)
		sem <- struct{}{}
		go func(u string) {
			defer wg.Done()
			defer func() { <-sem }()

			item, err := reg.Fetch(ctx, u, opts)
			if err != nil {
				errCh <- fmt.Errorf("fetch %s: %w", u, err)
				return
			}
			fname := safeFilename(u) + ext
			path := filepath.Join(dir, fname)
			f, err := os.Create(path)
			if err != nil {
				errCh <- err
				return
			}
			defer f.Close()
			if err := render.Item(f, item, format); err != nil {
				errCh <- err
			}
			if item != nil {
				itemCh <- *item
			}
			fmt.Fprintln(os.Stderr, "wrote", path)
		}(u)
	}
	wg.Wait()
	close(errCh)
	close(itemCh)
	var ingestQueue []core.Item
	for it := range itemCh {
		ingestQueue = append(ingestQueue, it)
	}
	ledger.Ingest(ctx, ingestQueue...)
	var firstErr error
	for e := range errCh {
		fmt.Fprintln(os.Stderr, e)
		if firstErr == nil {
			firstErr = e
		}
	}
	return firstErr
}

// searchFlags is parsed from `social-fetch search` args.
type searchFlags struct {
	provider       string
	max            int
	start          int
	cursor         string
	format         string
	output         string
	logFile        string
	before         *time.Time
	after          *time.Time
	includeDomains []string
	excludeDomains []string
	timeout        time.Duration
	query          string
}

func parseSearchFlags(args []string) (*searchFlags, error) {
	f := &searchFlags{
		provider: "duckduckgo",
		max:      10,
		format:   "markdown",
		timeout:  30 * time.Second,
	}
	var queryParts []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch a {
		case "-h", "--help":
			printSearchHelp(os.Stdout)
			os.Exit(0)
		case "-p", "--provider":
			i++
			if i >= len(args) {
				return nil, fmt.Errorf("--provider needs a value")
			}
			f.provider = args[i]
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
		case "--start":
			// 0-based result offset for paging through offset-
			// paginated providers (serpapi, hackernews, arxiv,
			// brave, google CSE). Ignored by cursor-paginated
			// providers — those use --cursor instead.
			i++
			if i >= len(args) {
				return nil, fmt.Errorf("--start needs a value")
			}
			n, err := atoi(args[i])
			if err != nil {
				return nil, err
			}
			f.start = n
		case "--cursor":
			// Opaque page token for cursor-paginated providers
			// (reddit, x, youtube, bluesky). On the first call
			// leave it unset; the previous call's `next_cursor`
			// is what to pass back here to continue paging.
			i++
			if i >= len(args) {
				return nil, fmt.Errorf("--cursor needs a value")
			}
			f.cursor = args[i]
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
		case "--timeout":
			i++
			if i >= len(args) {
				return nil, fmt.Errorf("--timeout needs a value")
			}
			d, err := time.ParseDuration(args[i])
			if err != nil {
				return nil, err
			}
			f.timeout = d
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
				// Allow shorthand like "7d" / "30d" that ParseDuration rejects.
				if dd, derr := parseDaysDuration(args[i]); derr == nil {
					d = dd
				} else {
					return nil, fmt.Errorf("--last: %w", err)
				}
			}
			t := time.Now().Add(-d)
			f.after = &t
		case "--site":
			i++
			if i >= len(args) {
				return nil, fmt.Errorf("--site needs a value")
			}
			f.includeDomains = append(f.includeDomains, args[i])
		case "--exclude-site":
			i++
			if i >= len(args) {
				return nil, fmt.Errorf("--exclude-site needs a value")
			}
			f.excludeDomains = append(f.excludeDomains, args[i])
		default:
			if strings.HasPrefix(a, "-") {
				return nil, fmt.Errorf("unknown flag %q", a)
			}
			queryParts = append(queryParts, a)
		}
	}
	f.query = strings.Join(queryParts, " ")
	return f, nil
}

// parseDateFlag accepts either a yyyy-mm-dd date or any RFC3339 timestamp
// so callers can be loose ("--after 2024-01-01") or precise.
func parseDateFlag(s string) (time.Time, error) {
	for _, layout := range []string{"2006-01-02", time.RFC3339, "2006-01-02T15:04:05Z"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("date %q must be yyyy-mm-dd or RFC3339", s)
}

// parseDaysDuration handles "Nd" (days) which time.ParseDuration rejects.
func parseDaysDuration(s string) (time.Duration, error) {
	if !strings.HasSuffix(s, "d") {
		return 0, fmt.Errorf("not a days value")
	}
	n, err := atoi(strings.TrimSuffix(s, "d"))
	if err != nil {
		return 0, err
	}
	return time.Duration(n) * 24 * time.Hour, nil
}

func runSearch(args []string) error {
	flags, err := parseSearchFlags(args)
	if err != nil {
		return err
	}
	if flags.query == "" {
		printSearchHelp(os.Stderr)
		return errors.New("no query given")
	}

	format, err := render.ParseFormat(flags.format)
	if err != nil {
		return err
	}

	audit, closeAudit, err := openAudit("search", flags.logFile)
	if err != nil {
		return err
	}
	defer closeAudit()

	provider, err := resolveSearcher(flags.provider)
	if err != nil {
		return err
	}

	ctx, cancel := signalContext(flags.timeout)
	ctx = core.WithAudit(ctx, audit)
	defer cancel()

	audit.Logf("search %q via %s (max=%d, start=%d)", flags.query, provider.Name(), flags.max, flags.start)
	searchOpts := core.SearchOptions{
		Max:            flags.max,
		Start:          flags.start,
		Cursor:         flags.cursor,
		Before:         flags.before,
		After:          flags.after,
		IncludeDomains: flags.includeDomains,
		ExcludeDomains: flags.excludeDomains,
	}
	var (
		results    []core.SearchResult
		nextCursor string
	)
	// Cursor-paginated providers (reddit, x, youtube, bluesky)
	// implement the optional CursorPaginator interface; use it
	// when available so we can surface next_cursor for the agent
	// to pass back on the next call.
	if cp, ok := provider.(core.CursorPaginator); ok {
		page, err := cp.SearchPaged(ctx, flags.query, searchOpts)
		if err != nil {
			audit.Logf("search FAILED: %v", err)
			return err
		}
		results = page.Results
		nextCursor = page.NextCursor
	} else {
		var err error
		results, err = provider.Search(ctx, flags.query, searchOpts)
		if err != nil {
			audit.Logf("search FAILED: %v", err)
			return err
		}
	}
	audit.Logf("search returned %d results (next_cursor=%q)", len(results), nextCursor)

	out, closeOut, err := openOutput(flags.output)
	if err != nil {
		return err
	}
	defer closeOut()

	return renderSearchResults(out, results, format, flags.query, provider.Name(), nextCursor)
}

// runAsk dispatches a question to one of the answer-engine providers
// (perplexity, grok) and renders the synthesized answer plus sources.
//
// Usage:
//
//	social-fetch ask "<question>" [-p perplexity|grok] [-m MODEL] [--last week|day|month|year]
func runAsk(args []string) error {
	var (
		question     string
		provider     = "perplexity"
		model        string
		recency      string
		instructions string
		formatStr    = "markdown"
		output       = "-"
		logFile      = ""
		maxTokens    int
		timeout      = 60 * time.Second
	)
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch a {
		case "-h", "--help":
			printAskHelp(os.Stdout)
			return nil
		case "-p", "--provider":
			i++
			if i >= len(args) {
				return errors.New("--provider needs a value")
			}
			provider = args[i]
		case "-m", "--model":
			i++
			if i >= len(args) {
				return errors.New("--model needs a value")
			}
			model = args[i]
		case "--last":
			i++
			if i >= len(args) {
				return errors.New("--last needs a value")
			}
			recency = args[i]
		case "-f", "--format":
			i++
			if i >= len(args) {
				return errors.New("--format needs a value")
			}
			formatStr = args[i]
		case "-o", "--output":
			i++
			if i >= len(args) {
				return errors.New("--output needs a value")
			}
			output = args[i]
		case "-l", "--log":
			i++
			if i >= len(args) {
				return errors.New("--log needs a value")
			}
			logFile = args[i]
		case "--max-tokens":
			i++
			if i >= len(args) {
				return errors.New("--max-tokens needs a value")
			}
			n, err := atoi(args[i])
			if err != nil {
				return fmt.Errorf("--max-tokens: %w", err)
			}
			maxTokens = n
		case "--instructions", "--system":
			i++
			if i >= len(args) {
				return errors.New("--instructions needs a value")
			}
			instructions = args[i]
		case "--timeout":
			i++
			if i >= len(args) {
				return errors.New("--timeout needs a value")
			}
			d, err := time.ParseDuration(args[i])
			if err != nil {
				return fmt.Errorf("--timeout: %w", err)
			}
			timeout = d
		default:
			if strings.HasPrefix(a, "-") {
				return fmt.Errorf("ask: unknown flag %q", a)
			}
			// First non-flag positional arg is the question; subsequent
			// ones are concatenated with spaces so users don't have to
			// quote multi-word questions on the shell.
			if question == "" {
				question = a
			} else {
				question += " " + a
			}
		}
	}
	if question == "" {
		printAskHelp(os.Stderr)
		return errors.New("no question given")
	}

	format, err := render.ParseFormat(formatStr)
	if err != nil {
		return err
	}
	audit, closeAudit, err := openAudit("ask", logFile)
	if err != nil {
		return err
	}
	defer closeAudit()

	asker, err := resolveAsker(provider)
	if err != nil {
		return err
	}

	ctx, cancel := signalContext(timeout)
	ctx = core.WithAudit(ctx, audit)
	defer cancel()

	audit.Logf("ask %q via %s (model=%s, recency=%s)", question, asker.Name(), model, recency)
	answer, err := asker.Ask(ctx, question, core.AskOptions{
		Model:        model,
		Recency:      recency,
		MaxTokens:    maxTokens,
		Instructions: instructions,
	})
	if err != nil {
		audit.Logf("ask FAILED: %v", err)
		return err
	}
	audit.Logf("ask returned answer (%d chars, %d sources)", len(answer.Text), len(answer.Sources))

	out, closeOut, err := openOutput(output)
	if err != nil {
		return err
	}
	defer closeOut()
	return renderAnswer(out, answer, format)
}

// renderAnswer writes an Answer in the requested format. Markdown gets
// the question as an H1, the synthesized text as the body, and a
// numbered Sources list at the bottom.
func renderAnswer(w io.Writer, a *core.Answer, format render.Format) error {
	switch format {
	case render.FormatJSON, render.FormatJSONL:
		// Reuse the search rendering envelope shape for consistency
		// with the rest of the CLI's JSON output.
		env := map[string]any{
			"written_at": time.Now().UTC(),
			"answer":     a,
		}
		body, err := json.MarshalIndent(env, "", "  ")
		if err != nil {
			return err
		}
		_, err = w.Write(body)
		return err
	case render.FormatMarkdown:
		fmt.Fprintf(w, "# Q: %s\n\n*Provider: %s", a.Question, a.Provider)
		if a.Model != "" {
			fmt.Fprintf(w, " (%s)", a.Model)
		}
		fmt.Fprintf(w, " · %d sources · %s*\n\n", len(a.Sources), a.Asked.Format(time.RFC3339))
		fmt.Fprintln(w, a.Text)
		if len(a.Sources) > 0 {
			fmt.Fprint(w, "\n## Sources\n\n")
			for i, s := range a.Sources {
				title := s.Title
				if title == "" {
					title = s.URL
				}
				fmt.Fprintf(w, "%d. [%s](%s)", i+1, title, s.URL)
				if s.Published != nil {
					fmt.Fprintf(w, " · *%s*", s.Published.Format("2006-01-02"))
				}
				fmt.Fprintln(w)
				if s.Snippet != "" {
					fmt.Fprintf(w, "   %s\n", s.Snippet)
				}
			}
		}
		return nil
	}
	return fmt.Errorf("unknown format %q", format)
}

func printAskHelp(w io.Writer) {
	fmt.Fprint(w, `social-fetch ask — pose a question to a grounded answer engine

Usage:
  social-fetch ask "<question>" [flags]

Flags:
  -p, --provider     NAME    perplexity (default), grok, openai, anthropic, google, tavily, serpapi
                             special values:
                               auto             try the built-in chain in order
                                                (perplexity → grok → openai → anthropic →
                                                 google → tavily → serpapi)
                               name1,name2,…    comma-list of providers to try in order;
                                                each falls through on missing key, error,
                                                or empty answer
  -m, --model        MODEL   override the provider's default; empty lets the
                             provider's API pick (recommended)
      --last         W       restrict the search horizon: day, week, month, year
      --max-tokens   N       cap response length
      --instructions S       system-prompt-like preamble; persistent guidance
                             ("always cite your sources", etc.).
                             Alias: --system
  -f, --format       FMT     markdown (default) or json
  -o, --output       PATH    -, FILE, or unset for stdout
  -l, --log          PATH    audit log destination
      --timeout      DUR     overall timeout (default 60s)
  -h, --help                 show this help

Auth:
  perplexity   PERPLEXITY_API_KEY (or PPLX_API_KEY)
  grok         XAI_API_KEY (or GROK_API_KEY)
  openai       OPENAI_API_KEY
  anthropic    ANTHROPIC_API_KEY
  google       GEMINI_API_KEY (or GOOGLE_API_KEY)
  tavily       TAVILY_API_KEY
  serpapi      SERPAPI_KEY

Output (markdown):
  # Q: ...
  *Provider: perplexity (sonar) · 5 sources · 2026-...*
  <synthesized answer>

  ## Sources
  1. [title](url)
  ...
`)
}

func renderSearchResults(w io.Writer, results []core.SearchResult, format render.Format, query, providerName, nextCursor string) error {
	switch format {
	case render.FormatJSON, render.FormatJSONL:
		// Wrap in a small envelope so consumers know which provider ran.
		env := map[string]any{
			"written_at": time.Now().UTC(),
			"query":      query,
			"provider":   providerName,
			"results":    results,
		}
		if nextCursor != "" {
			env["next_cursor"] = nextCursor
		}
		return render.Item(w, asItem(env, results, query, providerName), format)
	case render.FormatMarkdown:
		fmt.Fprintf(w, "# Search: %s\n\n*Provider: %s · %d results · %s*\n\n",
			query, providerName, len(results), time.Now().UTC().Format(time.RFC3339))
		for i, r := range results {
			fmt.Fprintf(w, "%d. [%s](%s)\n", i+1, displayTitle(r), r.URL)
			if r.Published != nil {
				fmt.Fprintf(w, "   *%s*\n", r.Published.Format("2006-01-02"))
			}
			if r.Snippet != "" {
				fmt.Fprintf(w, "   %s\n\n", r.Snippet)
			}
		}
		// Surface the next-page cursor so the agent can continue
		// paging by re-running with --cursor=<token>. Quiet when
		// unset (offset-paginated providers / last page reached).
		if nextCursor != "" {
			fmt.Fprintf(w, "\n---\n\n*More results available — re-run with `--cursor %s` to fetch the next page.*\n",
				nextCursor)
		}
		return nil
	}
	return fmt.Errorf("unknown format %q", format)
}

func displayTitle(r core.SearchResult) string {
	if r.Title != "" {
		return r.Title
	}
	return r.URL
}

// asItem reuses the render package by wrapping search results inside a
// core.Item so `render.Item` produces consistent envelope shape.
func asItem(_ map[string]any, results []core.SearchResult, query, provider string) *core.Item {
	children := make([]core.Item, 0, len(results))
	for _, r := range results {
		children = append(children, core.Item{
			Source:    r.Source,
			Kind:      "search-result",
			URL:       r.URL,
			Title:     r.Title,
			Summary:   r.Snippet,
			Published: r.Published,
		})
	}
	return &core.Item{
		Source:    provider,
		Kind:      "search",
		Title:     query,
		Children:  children,
		FetchedAt: time.Now().UTC(),
		Extra: map[string]any{
			"query":    query,
			"provider": provider,
			"count":    len(results),
		},
	}
}

func runHelp(args []string) error {
	if len(args) == 0 {
		printUsage(os.Stdout)
		return nil
	}
	switch args[0] {
	case "fetch":
		printFetchHelp(os.Stdout)
	case "search":
		printSearchHelp(os.Stdout)
	case "timeline":
		printTimelineHelp(os.Stdout)
	case "monitor":
		printMonitorHelp(os.Stdout)
	case "list":
		fmt.Fprintln(os.Stdout, "social-fetch list — print available fetch and search providers")
	default:
		printUsage(os.Stdout)
		return fmt.Errorf("no help topic for %q", args[0])
	}
	return nil
}

func runList() error {
	w := os.Stdout
	fmt.Fprintln(w, "Legend: [ok] = ready · [!auth] = missing API keys · [bridge] = needs local browser bridge")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Fetch providers (URL → Item):")
	writeFetchTable(w)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Search providers (query → []Result):")
	writeSearchTable(w)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Ask providers (question → grounded answer):")
	writeAskTable(w)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Timeline providers (user → activity feed):")
	writeTimelineTable(w)
	return nil
}

// writeFetchTable / writeSearchTable / writeTimelineTable enumerate the
// live registries and print one row per provider, so help text and
// `social-fetch list` always reflect what the binary actually supports.
// The auth-hint and example-URL lookup tables below stay hand-curated
// because the registries don't expose either, but they're keyed off
// the provider name so a missing entry just falls through to a blank
// — never produces stale or wrong text.

func writeFetchTable(w io.Writer) {
	fetchers, _ := buildRegistries()
	for _, f := range fetchers.Fetchers() {
		fmt.Fprintf(w, "  %s %-12s  %s%s\n",
			statusBadge("fetch", f.Name()),
			f.Name(),
			exampleFor(f.Name()),
			statusSuffix("fetch", f.Name()),
		)
	}
}

func writeSearchTable(w io.Writer) {
	_, searchers := buildRegistries()
	for _, p := range searchers.Providers() {
		fmt.Fprintf(w, "  %s %-12s  %s%s\n",
			statusBadge("search", p.Name()),
			p.Name(),
			searchAuthHint(p.Name()),
			statusSuffix("search", p.Name()),
		)
	}
}

func writeAskTable(w io.Writer) {
	for _, name := range buildAskers().Names() {
		fmt.Fprintf(w, "  %s %-12s  %s%s\n",
			statusBadge("ask", name),
			name,
			askAuthHint(name),
			statusSuffix("ask", name),
		)
	}
}

func writeTimelineTable(w io.Writer) {
	for _, name := range []string{"x", "linkedin"} {
		hint := ""
		switch name {
		case "x":
			hint = "(requires X_API_KEY + X_API_SECRET; recent 7d, kinds: all|tweets|replies|retweets)"
		case "linkedin":
			hint = "(requires bridge; kinds: all|posts|comments|reactions)"
		}
		fmt.Fprintf(w, "  %s %-12s  %s%s\n",
			statusBadge("timeline", name),
			name,
			hint,
			statusSuffix("timeline", name),
		)
	}
}

// statusSuffix appends the live availability reason to the right of
// each list row when the provider isn't fully configured. Empty for
// ready providers — keeps the [ok] rows clean.
func statusSuffix(category, name string) string {
	s := providerStatus(category, name)
	if s == "" {
		return ""
	}
	return "  → " + s
}

// askAuthHint mirrors searchAuthHint for the ask category. Each
// provider names its required env var so list output stays
// self-documenting; the actual availability check is in
// providerEnvReqs.
func askAuthHint(name string) string {
	switch name {
	case "perplexity":
		return "(requires PERPLEXITY_API_KEY; sonar / sonar-pro / sonar-reasoning)"
	case "grok":
		return "(requires XAI_API_KEY; live X data; grok-4 / grok-4-fast-reasoning)"
	case "openai":
		return "(requires OPENAI_API_KEY; gpt-5 / gpt-5-mini)"
	case "anthropic":
		return "(requires ANTHROPIC_API_KEY; claude-opus-4-7 / claude-sonnet-4-6)"
	case "gemini":
		return "(requires GEMINI_API_KEY or GOOGLE_API_KEY; gemini-2.5-pro / 2.5-flash; built-in google_search grounding)"
	case "tavily":
		return "(requires TAVILY_API_KEY; AI-tuned web search → synthesized answer)"
	case "serpapi":
		return "(requires SERPAPI_KEY; pulls Google's AI Overview block)"
	}
	return ""
}

func exampleFor(name string) string {
	switch name {
	case "hackernews":
		return "https://news.ycombinator.com/item?id=12345"
	case "reddit":
		return "https://www.reddit.com/r/<sub>/comments/<id>/<slug>/"
	case "github":
		return "https://github.com/<owner>/<repo>"
	case "twitter":
		return "https://x.com/<user>/status/<id>"
	case "linkedin":
		return "https://www.linkedin.com/posts/<user>-activity-<id> (bridge required)"
	case "youtube":
		return "https://www.youtube.com/watch?v=<id>"
	case "bluesky":
		return "https://bsky.app/profile/<handle>/post/<id>"
	case "arxiv":
		return "https://arxiv.org/abs/<id>"
	case "medium":
		return "https://medium.com/@<user>/<slug>"
	case "substack":
		return "https://<sub>.substack.com/p/<slug>"
	case "rss":
		return "https://example.com/feed.xml"
	case "article":
		return "any other http(s) URL (blogs, news, generic articles)"
	}
	return ""
}

func searchAuthHint(name string) string {
	switch name {
	case "duckduckgo":
		return "(no auth)"
	case "perplexity":
		return "(requires PERPLEXITY_API_KEY; raw search results, no LLM synthesis)"
	case "google":
		return "(requires GOOGLE_API_KEY + GOOGLE_CSE_ID; 100 q/day free. NOTE: Google removed 'Search the entire web' for new CSEs in 2024 — new keys are now restricted to your listed sites only. Use serpapi / brave / tavily for general web search.)"
	case "brave":
		return "(requires BRAVE_API_KEY; native --last 7d via freshness)"
	case "serpapi":
		return "(requires SERPAPI_KEY; 100 searches/month free)"
	case "tavily":
		return "(requires TAVILY_API_KEY; AI-tuned, 1k searches/month free)"
	case "hackernews":
		return "(no auth, Algolia)"
	case "reddit":
		return "(no auth, public search.json; rate-limited per IP)"
	case "x":
		return "(requires X_API_KEY + X_API_SECRET; recent 7d only)"
	case "youtube":
		return "(requires YOUTUBE_API_KEY; native --last 7d)"
	case "linkedin":
		return "(requires bridge + login; up to 50 results via scroll-to-bottom + wheel-event lazy-load. Use sparingly — LinkedIn rate-limits scraping accounts aggressively.)"
	case "bluesky":
		return "(requires BLUESKY_HANDLE + BLUESKY_APP_PASSWORD)"
	case "arxiv":
		return "(no auth, sorted newest-first)"
	}
	return ""
}

// ---- shared helpers ---------------------------------------------------

// collectURLs gathers URLs from positional args and an optional input file.
// If no positional args and no -i flag are given but stdin is a pipe (not
// a terminal), URLs are read from stdin automatically — so
// `cat urls.txt | social-fetch fetch` works without any flag.
func collectURLs(positional []string, inputFile string) ([]string, error) {
	urls := append([]string(nil), positional...)

	// Decide where, if anywhere, to read URLs from.
	switch {
	case inputFile != "":
		// explicit -i flag wins
	case len(positional) == 0 && stdinIsPipe():
		inputFile = "-"
	default:
		return urls, nil
	}

	var rd io.Reader
	if inputFile == "-" {
		rd = os.Stdin
	} else {
		f, err := os.Open(inputFile)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		rd = f
	}
	sc := bufio.NewScanner(rd)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		urls = append(urls, line)
	}
	return urls, sc.Err()
}

// stdinIsPipe reports whether stdin is being fed by a pipe or redirect
// rather than a terminal. Used to auto-enable batch mode.
func stdinIsPipe() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) == 0
}

func openOutput(target string) (io.Writer, func(), error) {
	if target == "" || target == "-" {
		return os.Stdout, func() {}, nil
	}
	if isDirOutput(target) {
		// Caller should have routed to fetchToDir; treating as file here
		// would be a bug.
		return nil, nil, fmt.Errorf("%q is a directory", target)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return nil, nil, err
	}
	f, err := os.Create(target)
	if err != nil {
		return nil, nil, err
	}
	return f, func() { _ = f.Close() }, nil
}

// isDirOutput is true when target ends with "/" or names an existing dir.
func isDirOutput(target string) bool {
	if target == "" || target == "-" {
		return false
	}
	if strings.HasSuffix(target, "/") {
		return true
	}
	if info, err := os.Stat(target); err == nil && info.IsDir() {
		return true
	}
	return false
}

func openLog(target string) (io.Writer, func(), error) {
	switch target {
	case "":
		return io.Discard, func() {}, nil
	case "-", "stderr":
		return os.Stderr, func() {}, nil
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return nil, nil, err
	}
	f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, nil, err
	}
	return f, func() { _ = f.Close() }, nil
}

// openAudit composes the user-facing audit destination (per -l/--log)
// with the always-on global audit log every social-fetch invocation
// appends to. Each Logf call writes to both. Use the returned close
// func to flush both sinks.
//
// cmd is the subcommand name ("fetch" / "search" / "timeline" / ...) —
// it lands in the global JSONL line so `social-fetch monitor` can
// distinguish concurrent invocations.
func openAudit(cmd, target string) (*core.AuditLogger, func(), error) {
	userW, closeUser, err := openLog(target)
	if err != nil {
		return nil, nil, err
	}
	globalW, closeGlobal, err := core.OpenGlobalAudit(cmd)
	if err != nil {
		// The user explicitly asked for an audit (or didn't disable
		// it) — surface the open failure but keep the user-facing
		// audit working so the run isn't abandoned over a cache-dir
		// hiccup.
		fmt.Fprintln(os.Stderr, "warning:", err)
	}
	audit := core.NewAuditLogger(userW)
	if globalW != nil {
		audit.AttachGlobal(globalW)
	}
	closeBoth := func() {
		closeUser()
		if closeGlobal != nil {
			closeGlobal()
		}
	}
	return audit, closeBoth, nil
}

func extFor(f render.Format) string {
	switch f {
	case render.FormatJSON, render.FormatJSONL:
		return ".json"
	case render.FormatMarkdown:
		return ".md"
	}
	return ".txt"
}

// safeFilename derives a stable, filesystem-safe name from a URL.
func safeFilename(raw string) string {
	s := strings.NewReplacer("://", "_", "/", "_", "?", "_", "&", "_", "=", "_", "#", "_", " ", "_").Replace(raw)
	if len(s) > 120 {
		s = s[:120]
	}
	return strings.Trim(s, "._-")
}

func atoi(s string) (int, error) {
	n := 0
	for i, c := range s {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("%q is not a number (offset %d)", s, i)
		}
		n = n*10 + int(c-'0')
	}
	return n, nil
}

// runBridge dispatches the bridge subcommands. With no subcommand it
// runs the daemon in the foreground (good for terminals and `nohup`);
// `start`/`stop` add background lifecycle control via a PID file.
//
//	social-fetch bridge run          run in foreground (default)
//	social-fetch bridge start        fork detached, write PID file
//	social-fetch bridge stop         SIGTERM the running daemon
//	social-fetch bridge status       check connection state
func runBridge(args []string) error {
	sub := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub = args[0]
		args = args[1:]
	}
	switch sub {
	case "", "run":
		return runBridgeForeground(args)
	case "start":
		return runBridgeStart(args)
	case "stop":
		return runBridgeStop(args)
	case "status", "ping":
		return runBridgeStatus(args)
	}
	if sub == "-h" || sub == "--help" {
		printBridgeHelp()
		return nil
	}
	return fmt.Errorf("bridge: unknown subcommand %q (try `bridge --help`)", sub)
}

func printBridgeHelp() {
	fmt.Print(`social-fetch bridge — control the browser-extension bridge

Usage:
  social-fetch bridge [run]         run in foreground (default)
  social-fetch bridge start         fork a detached daemon (writes PID file)
  social-fetch bridge stop          stop the running daemon
  social-fetch bridge status        report extension connection state

Common flags:
  --port N                         listen port (default 5555)
  --json                           machine-readable output (status only)

Endpoints (when running):
  ws://127.0.0.1:PORT/ws/extension     extension WebSocket
  http://127.0.0.1:PORT/cmd            JSON command POST
  http://127.0.0.1:PORT/status         JSON connection state

Exit codes (status):
  0   running and extension connected
  1   running but no extension attached
  2   bridge not reachable
`)
}

func runBridgeForeground(args []string) error {
	addr := fmt.Sprintf(":%d", bridge.DefaultPort)
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-h", "--help":
			printBridgeHelp()
			return nil
		case "--port":
			i++
			if i >= len(args) {
				return fmt.Errorf("--port needs a value")
			}
			n, err := atoi(args[i])
			if err != nil || n <= 0 || n > 65535 {
				return fmt.Errorf("--port: invalid value %q", args[i])
			}
			addr = fmt.Sprintf(":%d", n)
		default:
			return fmt.Errorf("bridge: unknown argument %q", args[i])
		}
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	srv := bridge.New()
	srv.Logf = func(format string, a ...any) {
		fmt.Fprintf(os.Stderr, "bridge: "+format+"\n", a...)
	}
	return srv.Run(ctx, addr)
}

// bridgeStateDir returns the directory we use for the PID file and the
// daemon log. We keep it under the user's cache dir so it doesn't live
// alongside source files; falls back to /tmp if the cache lookup fails.
func bridgeStateDir() string {
	if d, err := os.UserCacheDir(); err == nil {
		return filepath.Join(d, "social-fetch")
	}
	return filepath.Join(os.TempDir(), "social-fetch")
}

func bridgePIDFile() string { return filepath.Join(bridgeStateDir(), "bridge.pid") }
func bridgeLogFile() string { return filepath.Join(bridgeStateDir(), "bridge.log") }

// runBridgeStart spawns the binary as a detached child running the
// foreground bridge, then exits. Writes the child's PID to a file so a
// later `bridge stop` can find it. Refuses to start if a running PID
// is already on file (use `stop` first).
func runBridgeStart(args []string) error {
	port := bridge.DefaultPort
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--port":
			i++
			if i >= len(args) {
				return fmt.Errorf("--port needs a value")
			}
			n, err := atoi(args[i])
			if err != nil || n <= 0 || n > 65535 {
				return fmt.Errorf("--port: invalid value %q", args[i])
			}
			port = n
		case "-h", "--help":
			printBridgeHelp()
			return nil
		default:
			return fmt.Errorf("bridge start: unknown argument %q", args[i])
		}
	}

	if pid, ok := readBridgePID(); ok && processAlive(pid) {
		return fmt.Errorf("bridge already running (pid %d) — `social-fetch bridge stop` first", pid)
	}

	if err := os.MkdirAll(bridgeStateDir(), 0o755); err != nil {
		return err
	}
	logF, err := os.OpenFile(bridgeLogFile(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open log %s: %w", bridgeLogFile(), err)
	}

	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, "bridge", "run", "--port", fmt.Sprintf("%d", port))
	cmd.Stdout = logF
	cmd.Stderr = logF
	cmd.Stdin = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} // detach from terminal

	if err := cmd.Start(); err != nil {
		_ = logF.Close()
		return fmt.Errorf("spawn bridge: %w", err)
	}
	pid := cmd.Process.Pid
	// Don't reap the child — we want it to keep running after we exit.
	_ = cmd.Process.Release()
	_ = logF.Close()

	if err := os.WriteFile(bridgePIDFile(), fmt.Appendf(nil, "%d\n", pid), 0o644); err != nil {
		return fmt.Errorf("write pid file: %w", err)
	}

	// Tiny health check: poll /status for up to 2s. We require BOTH the
	// child PID to still be alive AND the port to answer, so a spawn
	// that loses the bind race against another process (we'd see only
	// `reachable=true` but the child exited) is reported as a failure.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !processAlive(pid) {
			_ = os.Remove(bridgePIDFile())
			return fmt.Errorf("bridge spawned (pid %d) but exited immediately — see %s (port %d may already be in use)",
				pid, bridgeLogFile(), port)
		}
		if reachable(port) {
			fmt.Printf("bridge started (pid %d, port %d, log %s)\n",
				pid, port, bridgeLogFile())
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("bridge spawned (pid %d) but didn't open :%d in 2s — check %s",
		pid, port, bridgeLogFile())
}

// runBridgeStop signals SIGTERM to the running daemon and removes the
// PID file. Idempotent: a missing PID file or a dead PID is reported,
// not an error.
func runBridgeStop(args []string) error {
	for i := 0; i < len(args); i++ {
		if args[i] == "-h" || args[i] == "--help" {
			printBridgeHelp()
			return nil
		}
		return fmt.Errorf("bridge stop: unknown argument %q", args[i])
	}

	pid, ok := readBridgePID()
	if !ok {
		fmt.Println("bridge not running (no pid file)")
		return nil
	}
	if !processAlive(pid) {
		fmt.Printf("bridge not running (stale pid %d) — clearing pid file\n", pid)
		_ = os.Remove(bridgePIDFile())
		return nil
	}
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		return fmt.Errorf("kill pid %d: %w", pid, err)
	}
	// Wait briefly for graceful shutdown.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if !processAlive(pid) {
			_ = os.Remove(bridgePIDFile())
			fmt.Printf("bridge stopped (pid %d)\n", pid)
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	// Still alive — escalate to SIGKILL.
	_ = syscall.Kill(pid, syscall.SIGKILL)
	_ = os.Remove(bridgePIDFile())
	fmt.Printf("bridge force-killed (pid %d)\n", pid)
	return nil
}

func readBridgePID() (int, bool) {
	b, err := os.ReadFile(bridgePIDFile())
	if err != nil {
		return 0, false
	}
	pid, err := atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, true
}

// processAlive reports whether a PID is a running process owned by us.
// `kill -0` is the standard probe; ESRCH means the process is gone.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}

func reachable(port int) bool {
	url := fmt.Sprintf("http://127.0.0.1:%d/status", port)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	resp, err := core.HTTPClient.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == 200
}

// runBridgeStatus probes the local bridge's /status endpoint and exits
// with a code that scripts (and the skill prompt) can branch on:
//
//	0  extension connected
//	1  bridge running, no extension
//	2  bridge unreachable (not running or wrong port)
//
// Plain stdout: "connected" / "not connected" / "bridge not running".
// With --json: emits {connected, reachable, port}.
func runBridgeStatus(args []string) error {
	port := bridge.DefaultPort
	asJSON := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--port":
			i++
			if i >= len(args) {
				return fmt.Errorf("--port needs a value")
			}
			n, err := atoi(args[i])
			if err != nil || n <= 0 || n > 65535 {
				return fmt.Errorf("--port: invalid value %q", args[i])
			}
			port = n
		case "--json":
			asJSON = true
		case "-h", "--help":
			fmt.Print(`social-fetch bridge status — probe the local bridge

Exits 0 if the extension is connected, 1 if the bridge is up but no
extension is attached, 2 if the bridge isn't reachable. Useful from
agents/skills as a precheck before fetching authenticated URLs.

Usage:
  social-fetch bridge status [--port N] [--json]
`)
			return nil
		default:
			return fmt.Errorf("bridge status: unknown argument %q", args[i])
		}
	}

	url := fmt.Sprintf("http://127.0.0.1:%d/status", port)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	resp, err := core.HTTPClient.Do(req)
	if err != nil {
		printStatus(asJSON, false, false, port)
		os.Exit(2)
	}
	defer resp.Body.Close()
	var body struct {
		Connected bool `json:"connected"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&body)

	printStatus(asJSON, true, body.Connected, port)
	if body.Connected {
		return nil
	}
	os.Exit(1)
	return nil
}

func printStatus(asJSON, reachable, connected bool, port int) {
	if asJSON {
		out, _ := json.Marshal(map[string]any{
			"reachable": reachable,
			"connected": connected,
			"port":      port,
		})
		fmt.Println(string(out))
		return
	}
	switch {
	case !reachable:
		fmt.Printf("bridge not running on :%d\n", port)
	case connected:
		fmt.Println("connected")
	default:
		fmt.Println("not connected")
	}
}

func signalContext(timeout time.Duration) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	return ctx, func() {
		stop()
		cancel()
	}
}

// ---- help text --------------------------------------------------------

// fetchTableString / searchTableString render the live registry tables
// into strings the help-text templates can interpolate via %s, so help
// stays in lockstep with what the binary actually supports.
func fetchTableString() string {
	var b strings.Builder
	writeFetchTable(&b)
	return strings.TrimRight(b.String(), "\n")
}

func searchTableString() string {
	var b strings.Builder
	writeSearchTable(&b)
	return strings.TrimRight(b.String(), "\n")
}

func printUsage(w io.Writer) {
	fmt.Fprintf(w, `social-fetch %s — fetch social-media URLs and run search queries

USAGE
  social-fetch fetch    <url> [<url>...] [flags]
  social-fetch search   "<query>" [flags]
  social-fetch timeline <user-or-url> [flags]   recent activity for a user (X / LinkedIn)
  social-fetch ask      "<question>" [flags]    grounded answer engine (perplexity, grok, openai, anthropic, google, tavily, serpapi)
  social-fetch research "<question>" [flags]    EXPERIMENTAL — multi-angle research workflow (decompose → fan-out → synthesize)
  social-fetch monitor  [flags]                 live tail of the global audit log
  social-fetch mcp                              run as MCP server on stdio (Claude Desktop Extension entry)
  social-fetch bridge   {start|stop|status|run}  control browser-extension bridge
  social-fetch list                              list fetch + search providers
  social-fetch help     [fetch|search|timeline|monitor|list]  same as --help on a subcommand
  social-fetch version                           print version

FETCH FLAGS
  -f, --format        FMT     markdown (default), json, jsonl
  -o, --output        PATH    "-" or unset = stdout
                              FILE          = single file
                              DIR/          = one file per URL
  -i, --input         FILE    URLs file, one per line ('-' = stdin)
                              '#' lines are comments
                              auto-detected when stdin is a pipe
  -j, --jobs          N       parallel fetch workers (default 4)
  -l, --log           PATH    audit/debug log ('-' or 'stderr' = stderr)
      --comments              include comment trees (default)
      --no-comments           skip comment trees (faster, smaller)
      --max-comments  N       cap total comments per item (0 = no cap)
      --generic-extraction    force the catch-all article extractor even
                              for Medium/Substack URLs (debug aid)
      --timeout       DUR     overall timeout, e.g. 60s, 2m (default 60s)

SEARCH FLAGS
  -p, --provider      NAME    pick a provider (see SEARCH PROVIDERS below;
                              default: duckduckgo)
  -n, --max           N       max results (default 10)
  -f, --format        FMT     markdown (default), json, or jsonl
  -o, --output        PATH    stdout or file
  -l, --log           PATH    audit log destination
      --after         DATE    yyyy-mm-dd or RFC3339; only newer hits
      --before        DATE    yyyy-mm-dd or RFC3339; only older hits
      --last          DUR     sugar for --after (e.g. 7d, 24h, 1m)
      --site          DOMAIN  restrict to domain (repeatable)
      --exclude-site  DOMAIN  exclude domain (repeatable)
      --timeout       DUR     overall timeout (default 30s)

FETCH SOURCES (auto-detected by URL host — run 'social-fetch list' for the live registry)
%s

SEARCH PROVIDERS (run 'social-fetch list' for the live registry)
%s

EXAMPLES
  social-fetch fetch https://news.ycombinator.com/item?id=43000000
  social-fetch fetch https://github.com/anthropics/claude-code -f markdown
  social-fetch fetch -i urls.txt -o out/ -f json -j 8 --no-comments
  cat urls.txt | social-fetch fetch -f jsonl > all.jsonl   # stdin auto-detected
  social-fetch search "vercel ai sdk" -p duckduckgo -n 5

NOTES FOR AGENTS
  - Default fetch format is markdown; use -f json or -f jsonl for machine input.
  - With multiple URLs and -f json, output is auto-promoted to jsonl.
  - --log - prints which URL was fetched and any redirects to stderr.
  - 'social-fetch list' prints the fetch + search providers in machine-friendly form.
`, Version, fetchTableString(), searchTableString())
}

func printFetchHelp(w io.Writer) {
	fmt.Fprintf(w, `social-fetch fetch — pull URLs from supported sources

Usage:
  social-fetch fetch <url> [<url>...] [flags]

Sources (auto-detected by URL host):
%s

Flags:
  -f, --format   FMT    Output format: markdown (default), json, jsonl
  -o, --output   PATH   "-" or unset = stdout; FILE = single file;
                        DIR/ = one file per URL (created if missing)
  -i, --input    FILE   Read URLs from FILE, one per line
                        ('-' = stdin, '#' lines are comments).
                        Stdin is auto-used when piped, even without -i.
  -j, --jobs     N      Parallel fetch workers (default 4). Output stays
                        in input order even with concurrency.
  -l, --log      PATH   Audit/debug log destination
                        ('-' or 'stderr' = stderr)
      --no-comments     Skip comment trees (HN, Reddit). Faster, smaller.
      --comments        Include comment trees (default)
      --max-comments N  Cap total comments per item (0 = no cap)
      --generic-extraction
                        Force the generic article extractor even for
                        Medium/Substack URLs. Useful when a host-specific
                        extractor's output looks wrong.
      --timeout DUR     Overall timeout, e.g. 60s, 2m (default 60s)
  -h, --help            Show this help

Examples:
  social-fetch fetch https://news.ycombinator.com/item?id=43000000
  social-fetch fetch https://x.com/jane/status/123 -f json
  social-fetch fetch -i bookmarks.txt -o ./out/ -f markdown -j 8 --no-comments
  cat urls.txt | social-fetch fetch -f jsonl > all.jsonl

Notes for agents:
  - Default format is markdown; pass -f json or -f jsonl for machine output.
  - With multiple URLs and -f json, output is automatically promoted to jsonl.
  - Use --log - to see exactly which URL the binary fetched and any redirects.
  - Output order matches input order even when -j > 1 (results buffered
    per-slot, written as each slot completes in sequence).
`, fetchTableString())
}

func printSearchHelp(w io.Writer) {
	fmt.Fprintf(w, `social-fetch search — run a query against a search provider

Usage:
  social-fetch search "<query>" [flags]

Flags:
  -p, --provider NAME   pick a provider (see below)
  -n, --max      N      Max results (default 10)
  -f, --format   FMT    markdown (default), json, or jsonl
  -o, --output   PATH   '-' or unset = stdout; otherwise file
  -l, --log      PATH   Audit/debug log destination
      --after  DATE     yyyy-mm-dd or RFC3339; only newer hits
      --before DATE     yyyy-mm-dd or RFC3339; only older hits
      --last   DUR      sugar for --after (e.g. 7d, 24h, 1m)
      --site DOMAIN     restrict to domain (repeatable)
      --exclude-site DOMAIN  exclude domain (repeatable)
      --timeout DUR     Overall timeout (default 30s)
  -h, --help            Show this help

Providers:
%s

Examples:
  social-fetch search "anthropic claude api" -n 5
  social-fetch search "harness engineering" -p reddit -n 20
  social-fetch search "ai harness" -p youtube --last 7d
  social-fetch search "rust async" -p hackernews -f jsonl -o results.jsonl
`, searchTableString())
}
