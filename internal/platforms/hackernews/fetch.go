// Package hackernews fetches stories and comment trees from Hacker News
// using the public Firebase API (https://github.com/HackerNews/API).
//
// Single-method today: only the `api` runner exists. The fetcher still
// routes through the shared fetchchain primitive so operators see the
// same SOCIAL_FETCH_CHAIN_<PLATFORM> knob across every fetcher; today
// the only valid value is `api`. The slot exists so a future second
// method (e.g. `jina` for an air-gapped fallback when the Firebase API
// is unreachable) lands without a breaking config change.
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

	"github.com/jedi4ever/social-skills/internal/core"
	"github.com/jedi4ever/social-skills/internal/fetchchain"
	"github.com/jedi4ever/social-skills/internal/util/htmlmd"
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

var defaultChain = []fetchchain.Method{fetchchain.MethodAPI}
var supportedMethods = map[fetchchain.Method]bool{fetchchain.MethodAPI: true}

// Fetch returns a populated Item for the given HN URL or ID.
func (f *Fetcher) Fetch(ctx context.Context, raw string, opts core.Options) (*core.Item, error) {
	ctx = core.WithAudit(ctx, opts.Audit)
	chain := fetchchain.Resolve(fetchchain.FromEnv("hackernews"), defaultChain, supportedMethods)
	runners := map[fetchchain.Method]fetchchain.Runner[*core.Item]{
		fetchchain.MethodAPI: func(ctx context.Context, raw string) (*core.Item, error) {
			return f.fetchViaAPI(ctx, raw, opts)
		},
	}
	item, _, err := fetchchain.Run(ctx, "hackernews", raw, opts.Audit, chain, runners)
	if err != nil {
		return nil, fmt.Errorf("hackernews: %w", err)
	}
	return item, nil
}

func (f *Fetcher) fetchViaAPI(ctx context.Context, raw string, opts core.Options) (*core.Item, error) {
	id, err := extractID(raw)
	if err != nil {
		return nil, err
	}

	story, err := f.fetchItem(ctx, id)
	if err != nil {
		return nil, err
	}
	if story == nil {
		return nil, fmt.Errorf("hackernews item %s not found", id)
	}

	var comments []core.Comment
	if opts.IncludeComments {
		// One bounded pool shared across the whole recursion. Acquired
		// only around the HTTP call, so a parent waiting on its kids
		// doesn't pin a slot — see commentWorkers' doc for why
		// per-level pools deadlock-throttled deep threads.
		pool := make(chan struct{}, commentWorkers)
		comments = f.fetchKids(ctx, story.Kids, 0, pool)
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
		Summary:     htmlmd.Convert(story.Text),
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

// commentWorkers caps the number of comment fetches in flight at once.
// Empirically 16 saturates the Firebase API without tripping rate limits;
// going higher just queues TCP connections.
//
// Earlier versions allocated one semaphore per recursion level. That
// looked like 16 wide at every depth but actually serialized fan-out:
// a parent goroutine held its parent-level slot while waiting on all
// its children, so one slow grandchild starved the parent's siblings.
// On deep, lopsided HN threads (the common case for popular stories)
// effective concurrency collapsed to 1 in the worst case. The fix is
// a single pool shared across the whole tree, acquired only for the
// HTTP round-trip — see fetchKids.
const commentWorkers = 16

// fetchKids walks the kid IDs in parallel up to MaxDepth, preserving
// order. The pool channel is shared with every recursion level so the
// HTTP-call concurrency cap is enforced globally; per-level pools
// would let parents pin slots while waiting on their children. The
// pool is acquired *inside* fetchComment around the actual fetchItem
// call, not here, so a parent goroutine releases its slot before
// recursing into kids.
func (f *Fetcher) fetchKids(ctx context.Context, ids []int, depth int, pool chan struct{}) []core.Comment {
	if depth >= f.MaxDepth || len(ids) == 0 {
		return nil
	}

	results := make([]core.Comment, len(ids))
	var wg sync.WaitGroup
	for i, id := range ids {
		wg.Add(1)
		go func(i int, id int) {
			defer wg.Done()
			c, err := f.fetchComment(ctx, id, depth, pool)
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

func (f *Fetcher) fetchComment(ctx context.Context, id, depth int, pool chan struct{}) (*core.Comment, error) {
	// Acquire only around the HTTP call so this goroutine isn't
	// holding a slot while its kids fetch. Releasing before recursing
	// is what keeps the global concurrency cap honest without
	// serializing the tree.
	pool <- struct{}{}
	item, err := f.fetchItem(ctx, strconv.Itoa(id))
	<-pool
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
		Body:      htmlmd.Convert(item.Text),
		Published: &t,
		Depth:     depth,
		Replies:   f.fetchKids(ctx, item.Kids, depth+1, pool),
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
