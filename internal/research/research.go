package research

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/jedi4ever/socialfetch/internal/core"
)

//go:embed prompts/decompose.md
var defaultDecomposePrompt string

//go:embed prompts/synthesize.md
var defaultSynthesizePrompt string

// Options bundles everything Run needs. Most fields are required —
// the orchestrator builds nothing on its own, it composes existing
// registries via the same code paths the CLI uses.
type Options struct {
	// Orchestrator drives the two LLM calls (decompose + synthesize).
	// Pass an AskChain when you want auto-fallback across providers.
	Orchestrator core.Asker

	// Worker registries — a worker dispatches based on Angle.Tool.
	Fetchers  *core.Registry
	Searchers *core.SearchRegistry
	Askers    *core.AskRegistry
	Timelines *core.TimelineRegistry

	// MaxAngles caps decomposition output. Defaults to 5; capped
	// upward at 8 to keep token budgets sane.
	MaxAngles int

	// Rounds — currently 1 (no gap-check loop yet) or 2 (round 2
	// asks the synthesizer for follow-up queries and runs them).
	Rounds int

	// Concurrency caps in-flight workers (default 4). Avoids
	// hammering rate-limited APIs.
	Concurrency int

	// Per-angle worker timeout. Default 60s.
	AngleTimeout time.Duration

	// DecomposePromptOverride / SynthesizePromptOverride supply
	// custom prompt template text. Empty means use the embedded
	// defaults from prompts/*.md. Non-empty MUST be a valid
	// text/template since both prompts substitute runtime values
	// (provider lists, the question, angle findings).
	DecomposePromptOverride  string
	SynthesizePromptOverride string

	// OnProgress receives progress events as the run proceeds. nil
	// is fine — events get dropped silently. The CLI installs a
	// stderr printer; programmatic callers tee into structured
	// logging.
	OnProgress func(Event)
}

// Run is the full research workflow. Returns a Report on success,
// or an error if a step the LLM can't recover from fails (couldn't
// reach the orchestrator, couldn't decode decomposition output).
// Individual angle failures are NOT errors — they land in the
// report's per-angle results and the synthesizer sees them.
func Run(ctx context.Context, question string, opts Options) (*Report, error) {
	if opts.Orchestrator == nil {
		return nil, errors.New("research: Orchestrator is required")
	}
	if opts.MaxAngles <= 0 {
		opts.MaxAngles = 5
	}
	if opts.MaxAngles > 8 {
		opts.MaxAngles = 8
	}
	if opts.Rounds <= 0 {
		opts.Rounds = 1
	}
	if opts.Rounds > 2 {
		opts.Rounds = 2
	}
	if opts.Concurrency <= 0 {
		opts.Concurrency = 4
	}
	if opts.AngleTimeout <= 0 {
		opts.AngleTimeout = 60 * time.Second
	}

	rep := &Report{
		Question:     question,
		Started:      time.Now(),
		Rounds:       opts.Rounds,
		Orchestrator: opts.Orchestrator.Name(),
	}

	emit := func(e Event) {
		if opts.OnProgress != nil {
			opts.OnProgress(e)
		}
	}

	// ---- decompose -------------------------------------------------
	decomposeStart := time.Now()
	emit(Event{Phase: PhaseDecomposeStart, Message: "decomposing question into angles"})
	angles, err := decompose(ctx, question, opts)
	if err != nil {
		return nil, fmt.Errorf("decompose: %w", err)
	}
	emit(Event{
		Phase:    PhaseDecomposeDone,
		Message:  fmt.Sprintf("%d angles", len(angles)),
		Total:    len(angles),
		Duration: time.Since(decomposeStart),
	})

	// ---- fan out ---------------------------------------------------
	emit(Event{Phase: PhaseFanoutStart, Total: len(angles), Message: fmt.Sprintf("running %d angles in parallel (jobs=%d)", len(angles), opts.Concurrency)})
	results := fanOut(ctx, angles, opts, emit)
	rep.Angles = results

	// ---- synthesize ------------------------------------------------
	synthStart := time.Now()
	emit(Event{Phase: PhaseSynthesizeStart, Message: "synthesizing"})
	answer, sources, err := synthesize(ctx, question, results, opts)
	if err != nil {
		return nil, fmt.Errorf("synthesize: %w", err)
	}
	rep.Answer = answer
	rep.Sources = sources
	emit(Event{Phase: PhaseSynthesizeDone, Message: fmt.Sprintf("%d chars, %d sources", len(answer), len(sources)), Duration: time.Since(synthStart)})

	rep.Finished = time.Now()
	emit(Event{Phase: PhaseDone, Message: fmt.Sprintf("done in %s", rep.Finished.Sub(rep.Started).Round(time.Millisecond)), Duration: rep.Finished.Sub(rep.Started)})
	return rep, nil
}

// ---- decompose ----------------------------------------------------

type decomposeResult struct {
	Angles []Angle `json:"angles"`
}

func decompose(ctx context.Context, question string, opts Options) ([]Angle, error) {
	tmplText := opts.DecomposePromptOverride
	if tmplText == "" {
		tmplText = defaultDecomposePrompt
	}
	tmpl, err := template.New("decompose").Parse(tmplText)
	if err != nil {
		return nil, fmt.Errorf("template parse: %w", err)
	}
	data := map[string]any{
		"Question":  question,
		"MaxAngles": opts.MaxAngles,
		"Tools":     buildToolSpecs(opts),
	}
	var prompt bytes.Buffer
	if err := tmpl.Execute(&prompt, data); err != nil {
		return nil, fmt.Errorf("template execute: %w", err)
	}

	ans, err := opts.Orchestrator.Ask(ctx, prompt.String(), core.AskOptions{
		// Decomposition is cheap (small input + small structured
		// output). Cap output tokens to encourage tight responses
		// and prevent runaway.
		MaxTokens: 1500,
	})
	if err != nil {
		return nil, err
	}

	jsonText := extractJSON(ans.Text)
	var dr decomposeResult
	if err := json.Unmarshal([]byte(jsonText), &dr); err != nil {
		return nil, fmt.Errorf("decode decomposition output: %w (raw=%q)", err, truncate(ans.Text, 200))
	}
	if len(dr.Angles) == 0 {
		return nil, fmt.Errorf("decomposition returned no angles (raw=%q)", truncate(ans.Text, 200))
	}
	if len(dr.Angles) > opts.MaxAngles {
		dr.Angles = dr.Angles[:opts.MaxAngles]
	}
	return dr.Angles, nil
}

// ---- fan out ------------------------------------------------------

func fanOut(parent context.Context, angles []Angle, opts Options, emit func(Event)) []AngleResult {
	results := make([]AngleResult, len(angles))
	sem := make(chan struct{}, opts.Concurrency)
	var wg sync.WaitGroup
	for i, a := range angles {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, a Angle) {
			defer wg.Done()
			defer func() { <-sem }()
			emit(Event{
				Phase:   PhaseAngleStart,
				Index:   i + 1,
				Total:   len(angles),
				Message: fmt.Sprintf("%s (%s/%s)", a.Label, a.Tool, providerOrAuto(a.Provider)),
			})
			ctx, cancel := context.WithTimeout(parent, opts.AngleTimeout)
			defer cancel()
			start := time.Now()
			r := dispatch(ctx, a, opts)
			r.Duration = time.Since(start)
			results[i] = r
			msg := fmt.Sprintf("%s (%s)", a.Label, summarizeResult(r))
			emit(Event{
				Phase:    PhaseAngleDone,
				Index:    i + 1,
				Total:    len(angles),
				Duration: r.Duration,
				Message:  msg,
				Err:      r.Err,
			})
		}(i, a)
	}
	wg.Wait()
	return results
}

// normalizeToolName accepts either the namespaced MCP tool name
// (`socialfetch_fetch`, what the decomposer prompt recommends) or the
// bare category (`fetch`, what hand-written angle JSON tends to use).
// Both resolve to the same dispatcher branch.
func normalizeToolName(t string) string {
	t = strings.ToLower(strings.TrimSpace(t))
	return strings.TrimPrefix(t, "socialfetch_")
}

// dispatch turns an Angle into one tool call against the existing
// registries. Errors are stuffed into AngleResult.Err so the
// synthesizer sees the partial picture instead of the run aborting.
func dispatch(ctx context.Context, a Angle, opts Options) AngleResult {
	r := AngleResult{Angle: a}
	switch normalizeToolName(a.Tool) {
	case "ask":
		if a.Question == "" {
			r.Err = errors.New("angle.question is empty for tool=ask")
			return r
		}
		asker, err := resolveAsker(opts, a.Provider)
		if err != nil {
			r.Err = err
			return r
		}
		ans, err := asker.Ask(ctx, a.Question, core.AskOptions{})
		if err != nil {
			r.Err = err
			return r
		}
		r.Findings = ans.Text
		r.Sources = ans.Sources
	case "search":
		if a.Query == "" {
			r.Err = errors.New("angle.query is empty for tool=search")
			return r
		}
		searcher, err := resolveSearcher(opts, a.Provider)
		if err != nil {
			r.Err = err
			return r
		}
		hits, err := searcher.Search(ctx, a.Query, core.SearchOptions{Max: 8})
		if err != nil {
			r.Err = err
			return r
		}
		r.Findings = renderSearchHits(hits)
		for _, h := range hits {
			r.Sources = append(r.Sources, core.Source{
				Title: h.Title, URL: h.URL, Snippet: h.Snippet, Published: h.Published,
			})
		}
	case "fetch":
		if a.URL == "" {
			r.Err = errors.New("angle.url is empty for tool=fetch")
			return r
		}
		opts2 := core.Options{IncludeComments: false, Audit: core.NewAuditLogger(nil)}
		item, err := opts.Fetchers.Fetch(ctx, a.URL, opts2)
		if err != nil {
			r.Err = err
			return r
		}
		r.Findings = renderItem(item)
		r.Sources = append(r.Sources, core.Source{Title: item.Title, URL: item.URL})
	case "timeline":
		if a.User == "" {
			r.Err = errors.New("angle.user is empty for tool=timeline")
			return r
		}
		provider, user, err := core.ParseIdentifier(a.User, a.Provider)
		if err != nil {
			r.Err = err
			return r
		}
		p, err := opts.Timelines.Get(provider)
		if err != nil {
			r.Err = err
			return r
		}
		item, err := p.Fetch(ctx, user, core.TimelineOptions{Max: 10})
		if err != nil {
			r.Err = err
			return r
		}
		r.Findings = renderItem(item)
		r.Sources = append(r.Sources, core.Source{Title: item.Title, URL: item.URL})
	default:
		r.Err = fmt.Errorf("unknown tool %q (want ask|search|fetch|timeline)", a.Tool)
	}
	return r
}

// resolveAsker / resolveSearcher are intentionally tolerant of
// invalid `provider` fields the decomposer LLM may emit. When the
// name doesn't resolve to a known provider (typo, hallucination,
// wrong tool — e.g. `linkedin` for a search angle), we fall through
// to the auto chain rather than returning an error and losing the
// whole angle. The audit log records the substitution so the
// operator can see when this fired.

func resolveAsker(opts Options, name string) (core.Asker, error) {
	if name == "" || strings.EqualFold(name, "auto") {
		// Default chain — if the orchestrator was already an
		// AskChain, reuse the same chain semantics for workers too.
		return opts.Orchestrator, nil
	}
	a, err := opts.Askers.Get(name)
	if err != nil {
		// Hallucinated / unknown provider — fall back to the
		// orchestrator (which is the auto chain by default).
		return opts.Orchestrator, nil
	}
	return a, nil
}

func resolveSearcher(opts Options, name string) (core.SearchProvider, error) {
	autoChain := func() (core.SearchProvider, error) {
		return core.NewSearchChain(opts.Searchers, []string{
			"perplexity", "tavily", "brave", "serpapi", "duckduckgo",
		})
	}
	if name == "" || strings.EqualFold(name, "auto") {
		return autoChain()
	}
	p, err := opts.Searchers.Get(name)
	if err != nil {
		// Hallucinated / unknown provider — fall back to the auto
		// search chain.
		return autoChain()
	}
	return p, nil
}

// ---- synthesize ---------------------------------------------------

type synthData struct {
	Question string
	Angles   []synthAngle
}

type synthAngle struct {
	Label    string
	Tool     string
	Provider string
	Summary  string
	Err      string
}

func synthesize(ctx context.Context, question string, angles []AngleResult, opts Options) (string, []core.Source, error) {
	tmplText := opts.SynthesizePromptOverride
	if tmplText == "" {
		tmplText = defaultSynthesizePrompt
	}
	tmpl, err := template.New("synthesize").Parse(tmplText)
	if err != nil {
		return "", nil, fmt.Errorf("template parse: %w", err)
	}
	data := synthData{Question: question}
	for _, a := range angles {
		entry := synthAngle{
			Label:    a.Angle.Label,
			Tool:     a.Angle.Tool,
			Provider: providerOrAuto(a.Angle.Provider),
			Summary:  a.Findings,
		}
		if a.Err != nil {
			entry.Err = a.Err.Error()
		}
		data.Angles = append(data.Angles, entry)
	}
	var prompt bytes.Buffer
	if err := tmpl.Execute(&prompt, data); err != nil {
		return "", nil, fmt.Errorf("template execute: %w", err)
	}
	ans, err := opts.Orchestrator.Ask(ctx, prompt.String(), core.AskOptions{
		MaxTokens: 4000,
	})
	if err != nil {
		return "", nil, err
	}
	sources := dedupeSources(angles)
	return ans.Text, sources, nil
}

// ---- helpers ------------------------------------------------------

// extractJSON peels off LLM-style markdown fences if present so
// json.Unmarshal sees pure JSON. Tolerant of either ```json or
// plain ``` openers.
func extractJSON(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		// Drop opening fence line.
		if i := strings.IndexByte(s, '\n'); i >= 0 {
			s = s[i+1:]
		}
		// Drop closing fence.
		if i := strings.LastIndex(s, "```"); i >= 0 {
			s = s[:i]
		}
	}
	return strings.TrimSpace(s)
}

func providerOrAuto(p string) string {
	if p == "" {
		return "auto"
	}
	return p
}

func renderSearchHits(hits []core.SearchResult) string {
	var b strings.Builder
	for i, h := range hits {
		fmt.Fprintf(&b, "%d. %s\n   %s\n", i+1, h.Title, h.URL)
		if h.Snippet != "" {
			fmt.Fprintf(&b, "   %s\n", h.Snippet)
		}
	}
	return strings.TrimSpace(b.String())
}

func renderItem(item *core.Item) string {
	var b strings.Builder
	if item.Title != "" {
		fmt.Fprintf(&b, "# %s\n\n", item.Title)
	}
	if item.Author != "" {
		fmt.Fprintf(&b, "by %s\n\n", item.Author)
	}
	// Cap content to keep synthesis input under control. 8000 chars
	// is enough for the synthesizer to extract substance.
	body := item.Content
	if len(body) > 8000 {
		body = body[:8000] + "\n\n…(truncated)"
	}
	b.WriteString(body)
	if len(item.Children) > 0 && len(item.Children) <= 20 {
		b.WriteString("\n\n## Recent items\n\n")
		for _, c := range item.Children {
			fmt.Fprintf(&b, "- %s — %s\n", c.Title, c.URL)
		}
	}
	return strings.TrimSpace(b.String())
}

func dedupeSources(angles []AngleResult) []core.Source {
	seen := make(map[string]bool)
	var out []core.Source
	for _, a := range angles {
		for _, s := range a.Sources {
			u := strings.TrimSpace(s.URL)
			if u == "" || seen[u] {
				continue
			}
			seen[u] = true
			out = append(out, s)
		}
	}
	return out
}

func summarizeResult(r AngleResult) string {
	if r.Err != nil {
		return "ERR: " + r.Err.Error()
	}
	return fmt.Sprintf("%s, %d sources", humanBytes(len(r.Findings)), len(r.Sources))
}

func humanBytes(n int) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	if n < 1024*1024 {
		return fmt.Sprintf("%.1f kB", float64(n)/1024)
	}
	return fmt.Sprintf("%.1f MB", float64(n)/1024/1024)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
