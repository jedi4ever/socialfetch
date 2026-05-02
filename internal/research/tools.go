package research

import "strings"

// ToolSpec is the per-tool metadata the decomposer prompt iterates
// over. Centralizing this here means a new MCP tool only needs to
// be registered in one place — the prompt template stays the same
// because it ranges over whatever buildToolSpecs returns.
//
// Name uses the namespaced MCP form (`socialfetch_*`) so the LLM
// sees the same vocabulary across the research command and the
// other MCP tools. The dispatcher accepts either the namespaced or
// the bare form (see normalizeToolName).
type ToolSpec struct {
	Name      string // the tool name the decomposer should emit
	Purpose   string // 1-2 sentence description of what the tool's for
	Providers string // pre-joined comma list of valid providers, "" if N/A
	Notes     string // multi-line caveats (no date operators, etc.)
}

// buildToolSpecs assembles the tool list from the live registries
// plus the static metadata the decomposer needs (purpose, caveats).
// Order matters — decomposer biases toward earlier tools when it
// sees overlap. Cheap-and-broad first (ask/search), then
// targeted (fetch/timeline).
func buildToolSpecs(opts Options) []ToolSpec {
	fetchNames := make([]string, 0)
	for _, f := range opts.Fetchers.Fetchers() {
		fetchNames = append(fetchNames, f.Name())
	}
	return []ToolSpec{
		{
			Name:      "socialfetch_ask",
			Purpose:   "grounded answer engine. Use for \"what is X\" or \"why does Y matter\" sub-questions where a synthesized paragraph is more useful than raw URLs.",
			Providers: strings.Join(opts.Askers.Names(), ", "),
		},
		{
			Name:      "socialfetch_search",
			Purpose:   "list of URLs + snippets for a query. Use for \"who is talking about X\" or \"where has Y been discussed\".",
			Providers: strings.Join(opts.Searchers.Names(), ", "),
			Notes: "IMPORTANT: do NOT include date operators like `before:`, `after:`, `since:`, `until:` in the query string — most providers reject them.\n" +
				"  IMPORTANT: `linkedin` is NOT a search provider. Don't pick it for search.",
		},
		{
			Name:    "socialfetch_fetch",
			Purpose: "the body of a single URL. Use only when you already know the URL is worth reading (a specific HN thread, GitHub repo, arXiv paper). Don't use for general queries; use search instead.",
			// `Providers` is the auto-detect fetcher list — the URL's
			// host picks among them. The decomposer doesn't pick a
			// provider here; we still surface the list so the LLM
			// knows what hosts are supported.
			Providers: strings.Join(fetchNames, ", "),
			Notes:     "No `provider` field needed — the URL's host auto-selects the right fetcher.",
		},
		{
			Name:      "socialfetch_timeline",
			Purpose:   "recent activity for a named person.",
			Providers: "x, linkedin",
			Notes:     "LinkedIn requires the local browser bridge; if you suspect the bridge isn't set up, prefer `socialfetch_search` provider=`x` instead.",
		},
	}
}
