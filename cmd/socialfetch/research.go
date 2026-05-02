// EXPERIMENTAL `research` subcommand. Not stable; flag set, prompt
// schema, and Report shape may change across releases.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/jedi4ever/socialfetch/internal/core"
	"github.com/jedi4ever/socialfetch/internal/platforms/linkedin"
	"github.com/jedi4ever/socialfetch/internal/platforms/twitter"
	"github.com/jedi4ever/socialfetch/internal/research"
)

type researchFlags struct {
	question     string
	orchestrator string // "auto" or a specific ask provider
	maxAngles    int
	rounds       int
	jobs         int
	angleTimeout time.Duration
	timeout      time.Duration
	output       string
	logFile      string
	decompPath   string // optional path to override decompose prompt
	synthPath    string // optional path to override synthesize prompt
	jsonReport   bool   // emit the structured Report as JSON instead of markdown
}

func parseResearchFlags(args []string) (*researchFlags, error) {
	f := &researchFlags{
		orchestrator: "auto",
		maxAngles:    5,
		rounds:       1,
		jobs:         4,
		angleTimeout: 60 * time.Second,
		timeout:      5 * time.Minute,
	}
	var qparts []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch a {
		case "-h", "--help":
			printResearchHelp(os.Stdout)
			os.Exit(0)
		case "--orchestrator":
			i++
			if i >= len(args) {
				return nil, errors.New("--orchestrator needs a value")
			}
			f.orchestrator = args[i]
		case "-n", "--max-angles", "--angles":
			i++
			if i >= len(args) {
				return nil, errors.New("--max-angles needs a value")
			}
			n, err := atoi(args[i])
			if err != nil {
				return nil, err
			}
			f.maxAngles = n
		case "--rounds":
			i++
			if i >= len(args) {
				return nil, errors.New("--rounds needs a value")
			}
			n, err := atoi(args[i])
			if err != nil {
				return nil, err
			}
			f.rounds = n
		case "-j", "--jobs":
			i++
			if i >= len(args) {
				return nil, errors.New("--jobs needs a value")
			}
			n, err := atoi(args[i])
			if err != nil {
				return nil, err
			}
			f.jobs = n
		case "--angle-timeout":
			i++
			if i >= len(args) {
				return nil, errors.New("--angle-timeout needs a value")
			}
			d, err := time.ParseDuration(args[i])
			if err != nil {
				return nil, err
			}
			f.angleTimeout = d
		case "--timeout":
			i++
			if i >= len(args) {
				return nil, errors.New("--timeout needs a value")
			}
			d, err := time.ParseDuration(args[i])
			if err != nil {
				return nil, err
			}
			f.timeout = d
		case "-o", "--output":
			i++
			if i >= len(args) {
				return nil, errors.New("--output needs a value")
			}
			f.output = args[i]
		case "-l", "--log":
			i++
			if i >= len(args) {
				return nil, errors.New("--log needs a value")
			}
			f.logFile = args[i]
		case "--prompt-decompose":
			i++
			if i >= len(args) {
				return nil, errors.New("--prompt-decompose needs a value")
			}
			f.decompPath = args[i]
		case "--prompt-synthesize":
			i++
			if i >= len(args) {
				return nil, errors.New("--prompt-synthesize needs a value")
			}
			f.synthPath = args[i]
		case "--json":
			f.jsonReport = true
		default:
			if strings.HasPrefix(a, "-") {
				return nil, fmt.Errorf("research: unknown flag %q", a)
			}
			qparts = append(qparts, a)
		}
	}
	f.question = strings.TrimSpace(strings.Join(qparts, " "))
	return f, nil
}

func runResearch(args []string) error {
	flags, err := parseResearchFlags(args)
	if err != nil {
		return err
	}
	if flags.question == "" {
		printResearchHelp(os.Stderr)
		return errors.New("no question given")
	}

	audit, closeAudit, err := openAudit("research", flags.logFile)
	if err != nil {
		return err
	}
	defer closeAudit()

	orchestrator, err := resolveAsker(flags.orchestrator)
	if err != nil {
		return err
	}

	fetchers, searchers := buildRegistries()
	askers := buildAskers()
	timelines := core.NewTimelineRegistry(
		twitter.NewXProvider(twitter.NewSearchProvider()),
		linkedin.NewLinkedInProvider(),
	)

	opts := research.Options{
		Orchestrator: orchestrator,
		Fetchers:     fetchers,
		Searchers:    searchers,
		Askers:       askers,
		Timelines:    timelines,
		MaxAngles:    flags.maxAngles,
		Rounds:       flags.rounds,
		Concurrency:  flags.jobs,
		AngleTimeout: flags.angleTimeout,
		OnProgress:   stderrProgress,
	}
	if flags.decompPath != "" {
		body, err := os.ReadFile(flags.decompPath)
		if err != nil {
			return fmt.Errorf("--prompt-decompose: %w", err)
		}
		opts.DecomposePromptOverride = string(body)
	}
	if flags.synthPath != "" {
		body, err := os.ReadFile(flags.synthPath)
		if err != nil {
			return fmt.Errorf("--prompt-synthesize: %w", err)
		}
		opts.SynthesizePromptOverride = string(body)
	}

	ctx, cancel := signalContext(flags.timeout)
	ctx = core.WithAudit(ctx, audit)
	defer cancel()

	audit.Logf("research %q via %s (angles=%d, jobs=%d, rounds=%d)",
		flags.question, orchestrator.Name(), flags.maxAngles, flags.jobs, flags.rounds)
	rep, err := research.Run(ctx, flags.question, opts)
	if err != nil {
		audit.Logf("research FAILED: %v", err)
		return err
	}
	audit.Logf("research done in %s — %d angles, %d sources, answer=%d chars",
		rep.Finished.Sub(rep.Started).Round(time.Millisecond),
		len(rep.Angles), len(rep.Sources), len(rep.Answer))

	out, closeOut, err := openOutput(flags.output)
	if err != nil {
		return err
	}
	defer closeOut()
	if flags.jsonReport {
		return renderResearchJSON(out, rep)
	}
	return renderResearchMarkdown(out, rep)
}

// stderrProgress prints one line per phase to stderr. Plain text — no
// ANSI escapes, no in-place updates — so it works in pipelines and
// redirected output without surprises. Each line carries a relative
// timestamp from the start of the run + a phase label.
func stderrProgress(e research.Event) {
	w := os.Stderr
	switch e.Phase {
	case research.PhaseDecomposeStart:
		fmt.Fprintln(w, "research: decomposing question into angles…")
	case research.PhaseDecomposeDone:
		fmt.Fprintf(w, "research: decomposed into %d angles in %s\n",
			e.Total, e.Duration.Round(time.Millisecond))
	case research.PhaseFanoutStart:
		fmt.Fprintf(w, "research: %s\n", e.Message)
	case research.PhaseAngleStart:
		fmt.Fprintf(w, "research: [%d/%d] start  %s\n", e.Index, e.Total, e.Message)
	case research.PhaseAngleDone:
		marker := "ok   "
		if e.Err != nil {
			marker = "FAIL "
		}
		fmt.Fprintf(w, "research: [%d/%d] %s%s in %s\n", e.Index, e.Total, marker, e.Message, e.Duration.Round(time.Millisecond))
	case research.PhaseSynthesizeStart:
		fmt.Fprintln(w, "research: synthesizing answer…")
	case research.PhaseSynthesizeDone:
		fmt.Fprintf(w, "research: synthesized %s in %s\n", e.Message, e.Duration.Round(time.Millisecond))
	case research.PhaseDone:
		fmt.Fprintf(w, "research: %s\n", e.Message)
	}
}

func renderResearchMarkdown(w io.Writer, r *research.Report) error {
	fmt.Fprintf(w, "# Research: %s\n\n", r.Question)
	fmt.Fprintf(w, "*Orchestrator: %s · %d angles · %s elapsed*\n\n",
		r.Orchestrator, len(r.Angles), r.Finished.Sub(r.Started).Round(time.Millisecond))
	fmt.Fprintln(w, r.Answer)
	if len(r.Angles) > 0 {
		fmt.Fprint(w, "\n---\n\n## Angle log\n\n")
		for i, a := range r.Angles {
			label := a.Angle.Label
			if label == "" {
				label = fmt.Sprintf("angle %d", i+1)
			}
			fmt.Fprintf(w, "%d. **%s** — `%s`", i+1, label, a.Angle.Tool)
			if a.Angle.Provider != "" {
				fmt.Fprintf(w, "/%s", a.Angle.Provider)
			}
			fmt.Fprintf(w, " (%s)", a.Duration.Round(time.Millisecond))
			if a.Err != nil {
				fmt.Fprintf(w, " — *err: %v*", a.Err)
			}
			fmt.Fprintln(w)
		}
	}
	return nil
}

func renderResearchJSON(w io.Writer, r *research.Report) error {
	body, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	_, err = w.Write(body)
	return err
}

func printResearchHelp(w io.Writer) {
	fmt.Fprint(w, `socialfetch research — EXPERIMENTAL multi-angle research workflow

Usage:
  socialfetch research "<question>" [flags]

What it does:
  1. Decomposes the question into 3–8 angles via an LLM call.
  2. Fans each angle out concurrently to fetch / search / ask /
     timeline using the existing socialfetch primitives.
  3. Synthesizes the findings into a markdown answer with citations.

EXPERIMENTAL — flag set, prompt schema, and Report shape may change
across releases. Pin a version if you depend on the exact output.

Flags:
      --orchestrator NAME   ask provider that drives decompose +
                            synthesize (default: auto = use the auto
                            chain perplexity → grok → openai →
                            anthropic → google → tavily → serpapi)
  -n, --max-angles N        cap decomposition output (default 5, max 8)
      --rounds N            1 (default) or 2; 2 enables a gap-fill
                            second pass (more thorough, ~2× tokens)
  -j, --jobs N              parallel angle workers (default 4)
      --angle-timeout DUR   per-angle worker timeout (default 60s)
      --timeout DUR         overall research timeout (default 5m)
  -o, --output PATH         '-' or unset = stdout, FILE = single file
  -l, --log PATH            audit log destination
      --json                emit the structured Report as JSON
                            instead of rendered markdown
      --prompt-decompose PATH    override the decomposition prompt
                                 (template; runs through text/template)
      --prompt-synthesize PATH   override the synthesis prompt
  -h, --help                show this help

Auth:
  Requires at least one ask-provider key for the orchestrator (with
  --orchestrator auto, any of: ANTHROPIC_API_KEY, OPENAI_API_KEY,
  PERPLEXITY_API_KEY, XAI_API_KEY, GEMINI/GOOGLE_API_KEY,
  TAVILY_API_KEY, SERPAPI_KEY). Workers use whatever providers their
  angles target — missing keys silently skip via the auto chains.

Progress:
  One line per phase printed to stderr while the run proceeds. Tail
  the global audit log with 'socialfetch monitor' for HTTP-level
  detail across all parallel workers.

Examples:
  socialfetch research "is the cline coding agent gaining traction"
  socialfetch research "what's harness engineering" --rounds 2 --jobs 6
  socialfetch research "..." --orchestrator anthropic    # force claude
  socialfetch research "..." --json -o report.json        # structured

Iterating on the prompts:
  Prompts live in internal/research/prompts/{decompose,synthesize}.md
  and are bundled at build time. Pass --prompt-decompose /
  --prompt-synthesize PATH to swap in a custom file at runtime
  without rebuilding.
`)
}
