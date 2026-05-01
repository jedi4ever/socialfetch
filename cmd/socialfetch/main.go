// Command socialfetch is a CLI for fetching social-media URLs (HackerNews,
// Reddit, GitHub, Twitter, Medium/Substack/articles, RSS) and search
// queries (DuckDuckGo, SerpAPI), and rendering the result as JSON, JSONL,
// or markdown.
//
// Subcommands:
//
//	socialfetch fetch <url> [<url>...]      fetch one or more URLs
//	socialfetch search "<query>"            run a search query
//	socialfetch list                        list fetchers and search providers
//	socialfetch help [sub]                  show help
//
// Run `socialfetch help` for the full reference.
package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/patrickdebois/social-skills/internal/core"
	"github.com/patrickdebois/social-skills/internal/render"
	"github.com/patrickdebois/social-skills/internal/search"

	"github.com/patrickdebois/social-skills/internal/sources/article"
	"github.com/patrickdebois/social-skills/internal/sources/github"
	"github.com/patrickdebois/social-skills/internal/sources/hackernews"
	"github.com/patrickdebois/social-skills/internal/sources/reddit"
	"github.com/patrickdebois/social-skills/internal/sources/rss"
	"github.com/patrickdebois/social-skills/internal/sources/twitter"

	"github.com/patrickdebois/social-skills/internal/search/duckduckgo"
	"github.com/patrickdebois/social-skills/internal/search/serpapi"
)

// buildRegistries wires up the default fetcher and search registries.
// The fetcher order matters: specific sources first, generic article last.
func buildRegistries() (*core.Registry, *search.Registry) {
	fetchers := core.NewRegistry(
		hackernews.New(),
		reddit.New(),
		github.New(),
		twitter.New(),
		rss.New(),
		article.New(), // catch-all — must be last
	)
	searchers := search.NewRegistry(
		duckduckgo.New(),
		serpapi.New(),
	)
	return fetchers, searchers
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
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
	case "search":
		return runSearch(rest)
	case "list":
		return runList()
	case "help", "-h", "--help":
		return runHelp(rest)
	case "version", "--version":
		fmt.Println("socialfetch 0.1.0")
		return nil
	default:
		printUsage(os.Stderr)
		return fmt.Errorf("unknown command %q", cmd)
	}
}

// fetchFlags is parsed from the args after `socialfetch fetch`.
type fetchFlags struct {
	format     string
	output     string // "" = stdout, file, or dir/
	inputFile  string // file with one URL per line
	logFile    string // audit/debug log destination ("-" = stderr)
	comments   bool
	maxComment int
	timeout    time.Duration
	urls       []string
}

func parseFetchFlags(args []string) (*fetchFlags, error) {
	f := &fetchFlags{
		format:   "markdown",
		comments: true,
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

	auditW, closeAudit, err := openLog(flags.logFile)
	if err != nil {
		return err
	}
	defer closeAudit()

	opts := core.Options{
		IncludeComments: flags.comments,
		MaxComments:     flags.maxComment,
		Audit:           core.NewAuditLogger(auditW),
	}

	registry, _ := buildRegistries()
	ctx, cancel := signalContext(flags.timeout)
	defer cancel()

	// Decide where output goes. For stdout / single file: stream as we go.
	// For a directory: write one file per URL.
	if isDirOutput(flags.output) {
		return fetchToDir(ctx, registry, urls, opts, format, flags.output)
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

	var firstErr error
	for _, u := range urls {
		item, ferr := registry.Fetch(ctx, u, opts)
		if ferr != nil {
			fmt.Fprintf(os.Stderr, "fetch %s: %v\n", u, ferr)
			if firstErr == nil {
				firstErr = ferr
			}
			continue
		}
		if err := render.Item(out, item, format); err != nil {
			return err
		}
		if format == render.FormatMarkdown && len(urls) > 1 {
			fmt.Fprint(out, "\n---\n\n")
		}
	}
	return firstErr
}

// fetchToDir writes one output file per URL into dir.
func fetchToDir(ctx context.Context, reg *core.Registry, urls []string, opts core.Options, format render.Format, dir string) error {
	if err := os.MkdirAll(strings.TrimRight(dir, "/"), 0o755); err != nil {
		return err
	}
	ext := extFor(format)

	var wg sync.WaitGroup
	errCh := make(chan error, len(urls))
	sem := make(chan struct{}, 4) // cap concurrency at 4

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
			fmt.Fprintln(os.Stderr, "wrote", path)
		}(u)
	}
	wg.Wait()
	close(errCh)
	var firstErr error
	for e := range errCh {
		fmt.Fprintln(os.Stderr, e)
		if firstErr == nil {
			firstErr = e
		}
	}
	return firstErr
}

// searchFlags is parsed from `socialfetch search` args.
type searchFlags struct {
	provider string
	max      int
	format   string
	output   string
	logFile  string
	timeout  time.Duration
	query    string
}

func parseSearchFlags(args []string) (*searchFlags, error) {
	f := &searchFlags{
		provider: "duckduckgo",
		max:      10,
		format:   "json",
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

	auditW, closeAudit, err := openLog(flags.logFile)
	if err != nil {
		return err
	}
	defer closeAudit()
	audit := core.NewAuditLogger(auditW)

	_, searchers := buildRegistries()
	provider, err := searchers.Get(flags.provider)
	if err != nil {
		return err
	}

	ctx, cancel := signalContext(flags.timeout)
	defer cancel()

	audit.Logf("search %q via %s (max=%d)", flags.query, provider.Name(), flags.max)
	results, err := provider.Search(ctx, flags.query, flags.max)
	if err != nil {
		audit.Logf("search FAILED: %v", err)
		return err
	}
	audit.Logf("search returned %d results", len(results))

	out, closeOut, err := openOutput(flags.output)
	if err != nil {
		return err
	}
	defer closeOut()

	return renderSearchResults(out, results, format, flags.query, provider.Name())
}

func renderSearchResults(w io.Writer, results []search.Result, format render.Format, query, providerName string) error {
	switch format {
	case render.FormatJSON, render.FormatJSONL:
		// Wrap in a small envelope so consumers know which provider ran.
		env := map[string]any{
			"written_at": time.Now().UTC(),
			"query":      query,
			"provider":   providerName,
			"results":    results,
		}
		return render.Item(w, asItem(env, results, query, providerName), format)
	case render.FormatMarkdown:
		fmt.Fprintf(w, "# Search: %s\n\n*Provider: %s · %d results · %s*\n\n",
			query, providerName, len(results), time.Now().UTC().Format(time.RFC3339))
		for i, r := range results {
			fmt.Fprintf(w, "%d. [%s](%s)\n", i+1, displayTitle(r), r.URL)
			if r.Snippet != "" {
				fmt.Fprintf(w, "   %s\n\n", r.Snippet)
			}
		}
		return nil
	}
	return fmt.Errorf("unknown format %q", format)
}

func displayTitle(r search.Result) string {
	if r.Title != "" {
		return r.Title
	}
	return r.URL
}

// asItem reuses the render package by wrapping search results inside a
// core.Item so `render.Item` produces consistent envelope shape.
func asItem(_ map[string]any, results []search.Result, query, provider string) *core.Item {
	children := make([]core.Item, 0, len(results))
	for _, r := range results {
		children = append(children, core.Item{
			Source:  r.Source,
			Kind:    "search-result",
			URL:     r.URL,
			Title:   r.Title,
			Summary: r.Snippet,
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
	case "list":
		fmt.Fprintln(os.Stdout, "socialfetch list — print available fetch and search providers")
	default:
		printUsage(os.Stdout)
		return fmt.Errorf("no help topic for %q", args[0])
	}
	return nil
}

func runList() error {
	fetchers, searchers := buildRegistries()
	w := os.Stdout

	fmt.Fprintln(w, "Fetch providers (URL → Item):")
	for _, f := range fetchers.Fetchers() {
		fmt.Fprintf(w, "  %-12s  %s\n", f.Name(), exampleFor(f.Name()))
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Search providers (query → []Result):")
	for _, p := range searchers.Providers() {
		auth := ""
		if p.Name() == "serpapi" {
			auth = "(requires SERPAPI_KEY)"
		}
		fmt.Fprintf(w, "  %-12s  %s\n", p.Name(), auth)
	}
	return nil
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
	case "rss":
		return "https://example.com/feed.xml"
	case "article":
		return "any other http(s) URL (Medium, Substack, blog post, ...)"
	}
	return ""
}

// ---- shared helpers ---------------------------------------------------

func collectURLs(positional []string, inputFile string) ([]string, error) {
	urls := append([]string(nil), positional...)
	if inputFile == "" {
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

func signalContext(timeout time.Duration) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	return ctx, func() {
		stop()
		cancel()
	}
}

// ---- help text --------------------------------------------------------

func printUsage(w io.Writer) {
	fmt.Fprint(w, `socialfetch — fetch social-media URLs and search queries

Usage:
  socialfetch fetch  <url> [<url>...] [flags]
  socialfetch search "<query>" [flags]
  socialfetch list                     list fetch + search providers
  socialfetch help [fetch|search]      detailed help for a subcommand
  socialfetch version                  print version

Quick examples:
  socialfetch fetch https://news.ycombinator.com/item?id=43000000
  socialfetch fetch https://github.com/anthropics/claude-code -f markdown
  socialfetch fetch -i urls.txt -o out/ -f json
  socialfetch search "vercel ai sdk" -p duckduckgo -n 5

Run 'socialfetch help fetch' or 'socialfetch help search' for full flags.
`)
}

func printFetchHelp(w io.Writer) {
	fmt.Fprint(w, `socialfetch fetch — pull URLs from supported sources

Usage:
  socialfetch fetch <url> [<url>...] [flags]

Sources are auto-detected by URL host. Run 'socialfetch list' to see them.

Flags:
  -f, --format   FMT    Output format: markdown (default), json, jsonl
  -o, --output   PATH   "-" or unset = stdout; FILE = single file;
                        DIR/ = one file per URL (created if missing)
  -i, --input    FILE   Read URLs from FILE, one per line
                        ('-' = stdin, '#' lines are comments)
  -l, --log      PATH   Audit/debug log destination
                        ('-' or 'stderr' = stderr)
      --no-comments     Skip comment trees (HN, Reddit). Faster, smaller.
      --comments        Include comment trees (default)
      --max-comments N  Cap total comments per item (0 = no cap)
      --timeout DUR     Overall timeout, e.g. 60s, 2m (default 60s)
  -h, --help            Show this help

Examples:
  socialfetch fetch https://news.ycombinator.com/item?id=43000000
  socialfetch fetch https://x.com/jane/status/123 -f json
  socialfetch fetch -i bookmarks.txt -o ./out/ -f markdown --no-comments
  cat urls.txt | socialfetch fetch -i - -f jsonl > all.jsonl

Notes for agents:
  - Default format is markdown; pass -f json or -f jsonl for machine output.
  - With multiple URLs and -f json, output is automatically promoted to jsonl.
  - Use --log - to see exactly which URL the binary fetched and any redirects.
`)
}

func printSearchHelp(w io.Writer) {
	fmt.Fprint(w, `socialfetch search — run a query against a search provider

Usage:
  socialfetch search "<query>" [flags]

Flags:
  -p, --provider NAME   Provider: duckduckgo (default) or serpapi
  -n, --max      N      Max results (default 10)
  -f, --format   FMT    json (default), jsonl, or markdown
  -o, --output   PATH   '-' or unset = stdout; otherwise file
  -l, --log      PATH   Audit/debug log destination
      --timeout  DUR    Overall timeout (default 30s)
  -h, --help            Show this help

Providers:
  duckduckgo  — no auth, scrapes the lite endpoint
  serpapi     — requires SERPAPI_KEY env var

Examples:
  socialfetch search "anthropic claude api" -n 5
  socialfetch search "openai" -p serpapi -f markdown
  socialfetch search "rust async" -f jsonl -o results.jsonl
`)
}
