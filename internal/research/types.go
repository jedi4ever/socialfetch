// Package research is the EXPERIMENTAL research orchestrator. It
// decomposes a question into angles via an Asker (LLM call), fans
// each angle out concurrently to the matching social-fetch primitive
// (fetch / search / ask / timeline), then synthesizes the findings
// into a single markdown report — also via an Asker call.
//
// "Experimental" means: the CLI flag, prompt files, and Report
// struct may change between releases. Pin a version if you depend
// on the exact shape.
//
// Prompts live in ./prompts/*.md and are bundled at build time via
// go:embed. Override at runtime via Options.DecomposePromptOverride
// / SynthesizePromptOverride (CLI flags --prompt-decompose /
// --prompt-synthesize) so the operator can iterate on prompt
// quality without rebuilding.
package research

import (
	"time"

	"github.com/jedi4ever/social-skills/internal/core"
)

// Angle is one piece of the decomposed question, executable as a
// single tool call.
type Angle struct {
	// Label is a human-readable short description (≤60 chars).
	Label string `json:"angle"`

	// Tool selects the dispatcher. One of: ask, search, fetch,
	// timeline.
	Tool string `json:"tool"`

	// Tool-specific fields. Only the field matching Tool is read;
	// the others are ignored.
	Query    string `json:"query,omitempty"`
	Question string `json:"question,omitempty"`
	URL      string `json:"url,omitempty"`
	User     string `json:"user,omitempty"`

	// Provider names a specific platform (e.g. "hackernews", "x",
	// "perplexity"). Empty means "use the auto chain". For
	// timeline, must be "x" or "linkedin".
	Provider string `json:"provider,omitempty"`
}

// AngleResult captures what one angle's worker produced. Either
// Findings has the worker output (as readable markdown) or Err is
// non-nil. Synthesis sees both — a failed angle is reported inline
// rather than silently dropped.
type AngleResult struct {
	Angle    Angle
	Findings string        // markdown summary the synthesizer reads
	Sources  []core.Source // citation list aggregated across the round
	Duration time.Duration
	Err      error
}

// Report is the full structured output of a research run. The CLI
// renders this as markdown; the MCP layer (when wired) will return
// it as JSON.
type Report struct {
	Question     string        `json:"question"`
	Answer       string        `json:"answer"` // synthesized markdown
	Sources      []core.Source `json:"sources,omitempty"`
	Angles       []AngleResult `json:"angles"`
	Started      time.Time     `json:"started"`
	Finished     time.Time     `json:"finished"`
	Rounds       int           `json:"rounds"`       // 1 or 2
	Orchestrator string        `json:"orchestrator"` // which Asker drove decompose/synth
}

// EventPhase enumerates the discrete steps of a research run. The
// CLI uses these to print progress lines; programmatic consumers
// can tee them into structured logging.
type EventPhase string

const (
	PhaseDecomposeStart  EventPhase = "decompose-start"
	PhaseDecomposeDone   EventPhase = "decompose-done"
	PhaseFanoutStart     EventPhase = "fanout-start"
	PhaseAngleStart      EventPhase = "angle-start"
	PhaseAngleDone       EventPhase = "angle-done"
	PhaseSynthesizeStart EventPhase = "synthesize-start"
	PhaseSynthesizeDone  EventPhase = "synthesize-done"
	PhaseDone            EventPhase = "done"
)

// Event is one progress notification fed to OnProgress.
type Event struct {
	Phase    EventPhase
	Message  string        // human-readable summary
	Index    int           // 1-based for angle-* events; 0 otherwise
	Total    int           // total angles for angle-* and fanout-start
	Duration time.Duration // populated on *-done events
	Err      error         // non-nil only on failure events
}
