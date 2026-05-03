// Package core defines the source-agnostic types and interfaces every
// fetcher implements. An Item is the common shape returned by all sources
// (HackerNews, Reddit, Twitter, articles, ...) so renderers and downstream
// tools never need to know which source produced it.
package core

import "time"

// Item is what any source returns: a single piece of social content with
// optional comments and media. Source-specific fields go in Extra.
type Item struct {
	Source string `json:"source"`
	Kind   string `json:"kind"`

	// URL is the canonical, post-redirect address — what the
	// fetcher considers the "real" location of this content.
	// For sources that route via API (HackerNews, GitHub, X v2)
	// this is the human-facing equivalent of the API resource;
	// for HTTP-fetched articles it's the URL after the redirect
	// chain settles.
	URL string `json:"url"`

	// RequestURL is the URL as the caller originally supplied
	// it, before any redirect or canonicalisation. Equals URL
	// for fetchers that don't redirect (most API-backed ones)
	// and differs when a t.co / bit.ly / 301 hop was followed.
	// Carried through the JSONL contract to social-ledger
	// so a `seen` lookup against the user-typed shortener URL
	// matches the stored canonical URL.
	//
	// Set automatically by core.Registry after each fetcher
	// returns, so per-source code rarely populates it
	// directly. Empty in JSON when equal to URL (omitempty).
	RequestURL string `json:"request_url,omitempty"`

	CanonicalID string         `json:"canonical_id,omitempty"`
	Title       string         `json:"title,omitempty"`
	Author      string         `json:"author,omitempty"`
	AuthorURL   string         `json:"author_url,omitempty"`
	Published   *time.Time     `json:"published,omitempty"`
	Summary     string         `json:"summary,omitempty"`
	Content     string         `json:"content,omitempty"`
	Score       int            `json:"score,omitempty"`
	Tags        []string       `json:"tags,omitempty"`
	Comments    []Comment      `json:"comments,omitempty"`
	Media       []Media        `json:"media,omitempty"`
	Children    []Item         `json:"children,omitempty"`
	FetchedAt   time.Time      `json:"fetched_at"`
	Extra       map[string]any `json:"extra,omitempty"`
}

// Comment is a reply on a story / post / article. Comments may nest via Replies.
type Comment struct {
	ID        string     `json:"id,omitempty"`
	Author    string     `json:"author,omitempty"`
	Body      string     `json:"body,omitempty"`
	Score     int        `json:"score,omitempty"`
	Published *time.Time `json:"published,omitempty"`
	Depth     int        `json:"depth"`
	Replies   []Comment  `json:"replies,omitempty"`
}

// Media is an image, video, or other asset linked from an item.
type Media struct {
	URL  string `json:"url"`
	Type string `json:"type,omitempty"`
	Alt  string `json:"alt,omitempty"`
}
