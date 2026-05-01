// Package core defines the source-agnostic types and interfaces every
// fetcher implements. An Item is the common shape returned by all sources
// (HackerNews, Reddit, Twitter, articles, ...) so renderers and downstream
// tools never need to know which source produced it.
package core

import "time"

// Item is what any source returns: a single piece of social content with
// optional comments and media. Source-specific fields go in Extra.
type Item struct {
	Source      string         `json:"source"`
	Kind        string         `json:"kind"`
	URL         string         `json:"url"`
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
