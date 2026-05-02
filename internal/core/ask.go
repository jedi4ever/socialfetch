// Package ask defines the Asker interface for "answer engines" —
// LLM-grounded services like Perplexity and Grok that take a natural-
// language question and return a synthesized answer plus citations.
//
// This is intentionally separate from internal/search (which returns
// a flat result list) because the conceptual shape is different: an
// Answer has a synthesized body that's the primary value, with sources
// as supporting metadata, while a search Result is one of many ranked
// hits with no synthesis between them.
package core

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Answer is what an Asker returns for a single question.
type Answer struct {
	Question string    `json:"question"`
	Provider string    `json:"provider"`
	Model    string    `json:"model,omitempty"`
	Text     string    `json:"text"`
	Sources  []Source  `json:"sources,omitempty"`
	Asked    time.Time `json:"asked"`
}

// Source is one citation referenced by the answer.
type Source struct {
	Title     string     `json:"title,omitempty"`
	URL       string     `json:"url"`
	Snippet   string     `json:"snippet,omitempty"`
	Published *time.Time `json:"published,omitempty"`
}

// Options shape a single Ask call.
type AskOptions struct {
	// Model overrides the provider's default. Examples:
	//   perplexity: "sonar", "sonar-pro", "sonar-reasoning"
	//   grok:       "grok-4.3", "grok-4-fast-reasoning"
	// Empty string means: let the provider (or its API) pick. Some
	// providers send no `model` field at all in that case so the
	// upstream account default applies — see grok.ask.
	Model string

	// MaxTokens caps the response length. Zero means provider default.
	MaxTokens int

	// Recency narrows the search horizon when the provider supports
	// it ("day", "week", "month", "year"). Empty means no filter.
	Recency string

	// Instructions is a system-prompt-like preamble passed to the
	// provider. Use it for persistent, query-independent guidance —
	// "always cite your sources", "prefer authoritative outlets",
	// "reject sources older than 12 months". Maps to:
	//   grok:       request.instructions
	//   perplexity: a system-role message prepended to messages
	//   google:     systemInstruction on the Gemini request
	// Empty means no extra instruction.
	Instructions string
}

// Asker is implemented by every backend.
type Asker interface {
	Name() string
	Ask(ctx context.Context, question string, opts AskOptions) (*Answer, error)
}

// Registry holds the registered askers, queried by name.
type AskRegistry struct {
	askers []Asker
}

func NewAskRegistry(askers ...Asker) *AskRegistry {
	return &AskRegistry{askers: askers}
}

// askAliases maps common synonyms to canonical ask provider names.
// Lowercase keys + values. See SearchRegistry.searchAliases for the
// rationale.
var askAliases = map[string]string{
	"claude":  "anthropic",
	"gpt":     "openai",
	"chatgpt": "openai",
	"sonar":   "perplexity",
	"pplx":    "perplexity",
	"gemini":  "google",
	"xai":     "grok",
}

func (r *AskRegistry) Get(name string) (Asker, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	if alias, ok := askAliases[name]; ok {
		name = alias
	}
	for _, a := range r.askers {
		if strings.EqualFold(a.Name(), name) {
			return a, nil
		}
	}
	return nil, fmt.Errorf("unknown ask provider %q (known: %s)", name, strings.Join(r.Names(), ", "))
}

func (r *AskRegistry) Names() []string {
	out := make([]string, 0, len(r.askers))
	for _, a := range r.askers {
		out = append(out, a.Name())
	}
	return out
}

func (r *AskRegistry) Askers() []Asker {
	out := make([]Asker, len(r.askers))
	copy(out, r.askers)
	return out
}
