// Package hackernews fetches stories and comment trees from Hacker News
// using the public Firebase API (https://github.com/HackerNews/API).
package hackernews

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/patrickdebois/social-skills/internal/core"
)

const defaultBaseURL = "https://hacker-news.firebaseio.com/v0"

// Maximum comment depth to traverse. Three is enough for the top of the
// thread and matches what the Python downloader does.
const defaultMaxDepth = 3

// Fetcher pulls a single story and its comment tree from HN.
type Fetcher struct {
	BaseURL  string
	MaxDepth int
}

// New returns a Fetcher with sensible defaults.
func New() *Fetcher {
	return &Fetcher{BaseURL: defaultBaseURL, MaxDepth: defaultMaxDepth}
}

func (Fetcher) Name() string { return "hackernews" }

// Match accepts both news.ycombinator.com URLs and bare numeric IDs the user
// might paste from HN.
func (Fetcher) Match(u *url.URL) bool {
	if u == nil {
		return false
	}
	if u.Host == "news.ycombinator.com" {
		return true
	}
	// Bare numeric ID (no scheme/host) — accepted as a convenience.
	if u.Scheme == "" && u.Host == "" && idFromString(u.Path) != "" {
		return true
	}
	return false
}

var idRE = regexp.MustCompile(`^\d+$`)

func idFromString(s string) string {
	if idRE.MatchString(s) {
		return s
	}
	return ""
}

// extractID pulls the story ID out of either a URL like
// https://news.ycombinator.com/item?id=12345 or a bare "12345".
func extractID(raw string) (string, error) {
	if id := idFromString(raw); id != "" {
		return id, nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	if id := u.Query().Get("id"); id != "" && idRE.MatchString(id) {
		return id, nil
	}
	return "", fmt.Errorf("no hackernews item id in %q", raw)
}

// hnItem mirrors the JSON returned by /v0/item/<id>.json.
type hnItem struct {
	ID          int64  `json:"id"`
	Type        string `json:"type"`
	By          string `json:"by"`
	Time        int64  `json:"time"`
	Title       string `json:"title"`
	Text        string `json:"text"`
	URL         string `json:"url"`
	Score       int    `json:"score"`
	Descendants int    `json:"descendants"`
	Kids        []int  `json:"kids"`
	Deleted     bool   `json:"deleted"`
	Dead        bool   `json:"dead"`
}

// Fetch returns a populated Item for the given HN URL or ID.
func (f *Fetcher) Fetch(ctx context.Context, raw string, opts core.Options) (*core.Item, error) {
	id, err := extractID(raw)
	if err != nil {
		return nil, err
	}
	ctx = core.WithAudit(ctx, opts.Audit)

	story, err := f.fetchItem(ctx, id)
	if err != nil {
		return nil, err
	}
	if story == nil {
		return nil, fmt.Errorf("hackernews item %s not found", id)
	}

	var comments []core.Comment
	if opts.IncludeComments {
		comments = f.fetchKids(ctx, story.Kids, 0)
		if opts.MaxComments > 0 {
			comments = capComments(comments, opts.MaxComments)
		}
	}

	published := time.Unix(story.Time, 0).UTC()
	item := &core.Item{
		Source:      "hackernews",
		Kind:        story.Type,
		URL:         fmt.Sprintf("https://news.ycombinator.com/item?id=%d", story.ID),
		CanonicalID: strconv.FormatInt(story.ID, 10),
		Title:       story.Title,
		Author:      story.By,
		AuthorURL:   "https://news.ycombinator.com/user?id=" + story.By,
		Published:   &published,
		Summary:     story.Text,
		Score:       story.Score,
		Comments:    comments,
		FetchedAt:   time.Now().UTC(),
		Extra: map[string]any{
			"hn_id":         story.ID,
			"linked_url":    story.URL,
			"descendants":   story.Descendants,
			"comment_count": countComments(comments),
		},
	}
	return item, nil
}

func (f *Fetcher) fetchItem(ctx context.Context, id string) (*hnItem, error) {
	var item hnItem
	url := fmt.Sprintf("%s/item/%s.json", f.BaseURL, id)
	if err := core.GetJSON(ctx, url, &item); err != nil {
		return nil, err
	}
	if item.ID == 0 {
		// HN returns the literal JSON value `null` for unknown IDs, which
		// decodes to a zero struct. Surface that as "not found".
		return nil, nil
	}
	return &item, nil
}

// fetchKids walks the kid IDs in parallel up to MaxDepth, preserving order.
func (f *Fetcher) fetchKids(ctx context.Context, ids []int, depth int) []core.Comment {
	if depth >= f.MaxDepth || len(ids) == 0 {
		return nil
	}

	results := make([]core.Comment, len(ids))
	var wg sync.WaitGroup
	for i, id := range ids {
		wg.Add(1)
		go func(i int, id int) {
			defer wg.Done()
			c, err := f.fetchComment(ctx, id, depth)
			if err == nil && c != nil {
				results[i] = *c
			}
		}(i, id)
	}
	wg.Wait()

	// Drop any zero-value entries (deleted/dead/errored items).
	out := results[:0]
	for _, c := range results {
		if c.ID != "" || c.Body != "" {
			out = append(out, c)
		}
	}
	return out
}

func (f *Fetcher) fetchComment(ctx context.Context, id, depth int) (*core.Comment, error) {
	item, err := f.fetchItem(ctx, strconv.Itoa(id))
	if err != nil {
		return nil, err
	}
	if item == nil || item.Deleted || item.Dead {
		return nil, errors.New("skipped")
	}

	t := time.Unix(item.Time, 0).UTC()
	c := &core.Comment{
		ID:        strconv.FormatInt(item.ID, 10),
		Author:    item.By,
		Body:      item.Text,
		Published: &t,
		Depth:     depth,
		Replies:   f.fetchKids(ctx, item.Kids, depth+1),
	}
	return c, nil
}

func countComments(cs []core.Comment) int {
	n := len(cs)
	for _, c := range cs {
		n += countComments(c.Replies)
	}
	return n
}

// capComments truncates the tree at a maximum total node count, walking
// breadth-first so nearer comments are kept.
func capComments(cs []core.Comment, max int) []core.Comment {
	if countComments(cs) <= max {
		return cs
	}
	remaining := max
	var trim func([]core.Comment) []core.Comment
	trim = func(in []core.Comment) []core.Comment {
		out := in[:0]
		for _, c := range in {
			if remaining == 0 {
				break
			}
			remaining--
			c.Replies = trim(c.Replies)
			out = append(out, c)
		}
		return out
	}
	return trim(cs)
}
