// Package reddit fetches a single Reddit post (with comment tree) using
// Reddit's public ".json" endpoint — appending .json to any post URL
// returns its data without auth.
package reddit

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/patrickdebois/social-skills/internal/core"
)

const defaultMaxDepth = 5

// Fetcher pulls a Reddit post and its comments.
type Fetcher struct {
	// BaseURL is set by tests to redirect requests at an httptest server.
	// In production it stays empty and we use the post URL as-is.
	BaseURL  string
	MaxDepth int
}

func New() *Fetcher {
	return &Fetcher{MaxDepth: defaultMaxDepth}
}

func (Fetcher) Name() string { return "reddit" }

func (Fetcher) Match(u *url.URL) bool {
	if u == nil {
		return false
	}
	host := strings.TrimPrefix(u.Host, "www.")
	if host != "reddit.com" && host != "old.reddit.com" {
		return false
	}
	return strings.Contains(u.Path, "/comments/")
}

// Reddit's JSON response is `[postListing, commentListing]`. We model only
// the fields we need — extra fields decode silently.
type listing struct {
	Kind string `json:"kind"`
	Data struct {
		Children []child `json:"children"`
	} `json:"data"`
}

type child struct {
	Kind string          `json:"kind"`
	Data json.RawMessage `json:"data"`
}

type postData struct {
	ID          string  `json:"id"`
	Title       string  `json:"title"`
	URL         string  `json:"url"`
	Selftext    string  `json:"selftext"`
	Author      string  `json:"author"`
	Subreddit   string  `json:"subreddit"`
	Score       int     `json:"score"`
	UpvoteRatio float64 `json:"upvote_ratio"`
	NumComments int     `json:"num_comments"`
	CreatedUTC  float64 `json:"created_utc"`
	Permalink   string  `json:"permalink"`
	IsSelf      bool    `json:"is_self"`
	LinkFlair   string  `json:"link_flair_text"`
	Preview     preview `json:"preview"`
}

type preview struct {
	Images []struct {
		Source struct {
			URL string `json:"url"`
		} `json:"source"`
	} `json:"images"`
}

type commentData struct {
	ID         string          `json:"id"`
	Author     string          `json:"author"`
	Body       string          `json:"body"`
	Score      int             `json:"score"`
	CreatedUTC float64         `json:"created_utc"`
	Replies    json.RawMessage `json:"replies"`
}

func (f *Fetcher) Fetch(ctx context.Context, raw string, opts core.Options) (*core.Item, error) {
	jsonURL, err := jsonURLFor(raw, f.BaseURL)
	if err != nil {
		return nil, err
	}
	ctx = core.WithAudit(ctx, opts.Audit)

	var listings []listing
	if err := core.GetJSON(ctx, jsonURL, &listings); err != nil {
		return nil, fmt.Errorf("reddit: %w", err)
	}
	if len(listings) < 1 || len(listings[0].Data.Children) == 0 {
		return nil, fmt.Errorf("reddit: no post in response")
	}

	var post postData
	if err := json.Unmarshal(listings[0].Data.Children[0].Data, &post); err != nil {
		return nil, fmt.Errorf("reddit: decode post: %w", err)
	}

	var comments []core.Comment
	if opts.IncludeComments && len(listings) > 1 {
		comments = extractComments(listings[1].Data.Children, 0, f.MaxDepth)
	}

	published := time.Unix(int64(post.CreatedUTC), 0).UTC()
	media := []core.Media{}
	for _, img := range post.Preview.Images {
		if img.Source.URL != "" {
			// Reddit serves &amp; in JSON; unescape so the URL is usable.
			media = append(media, core.Media{
				URL:  strings.ReplaceAll(img.Source.URL, "&amp;", "&"),
				Type: "image",
			})
		}
	}

	item := &core.Item{
		Source:      "reddit",
		Kind:        "post",
		URL:         "https://www.reddit.com" + post.Permalink,
		CanonicalID: post.ID,
		Title:       post.Title,
		Author:      post.Author,
		AuthorURL:   "https://www.reddit.com/user/" + post.Author,
		Published:   &published,
		Summary:     post.Selftext,
		Content:     post.Selftext,
		Score:       post.Score,
		Tags:        nonEmpty([]string{post.LinkFlair, "r/" + post.Subreddit}),
		Comments:    comments,
		Media:       media,
		FetchedAt:   time.Now().UTC(),
		Extra: map[string]any{
			"subreddit":    post.Subreddit,
			"upvote_ratio": post.UpvoteRatio,
			"num_comments": post.NumComments,
			"is_self":      post.IsSelf,
			"linked_url":   post.URL,
		},
	}
	return item, nil
}

// jsonURLFor turns a permalink into its .json equivalent. If override is set
// (tests) we swap the host/scheme to point at the test server while keeping
// the path so routing on the test server still works.
func jsonURLFor(raw, override string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	if !strings.HasSuffix(u.Path, ".json") {
		u.Path = strings.TrimRight(u.Path, "/") + ".json"
	}
	if override != "" {
		ov, err := url.Parse(override)
		if err != nil {
			return "", err
		}
		u.Scheme = ov.Scheme
		u.Host = ov.Host
	}
	q := u.Query()
	q.Set("limit", "200")
	q.Set("raw_json", "1")
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func extractComments(children []child, depth, maxDepth int) []core.Comment {
	if depth >= maxDepth {
		return nil
	}
	var out []core.Comment
	for _, ch := range children {
		if ch.Kind != "t1" {
			continue // skip "more" stubs and unknown kinds
		}
		var c commentData
		if err := json.Unmarshal(ch.Data, &c); err != nil {
			continue
		}
		t := time.Unix(int64(c.CreatedUTC), 0).UTC()
		var replies []core.Comment
		if len(c.Replies) > 0 && string(c.Replies) != `""` && string(c.Replies) != `null` {
			var nested listing
			if err := json.Unmarshal(c.Replies, &nested); err == nil {
				replies = extractComments(nested.Data.Children, depth+1, maxDepth)
			}
		}
		out = append(out, core.Comment{
			ID:        c.ID,
			Author:    c.Author,
			Body:      c.Body,
			Score:     c.Score,
			Published: &t,
			Depth:     depth,
			Replies:   replies,
		})
	}
	return out
}

func nonEmpty(in []string) []string {
	out := in[:0]
	for _, s := range in {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// Permalink-shaped guard so we don't accidentally accept things like /r/foo/.
var permalinkRE = regexp.MustCompile(`/comments/[a-z0-9]+`)

// IsPermalink reports whether u points at a single post (not a subreddit).
// Exposed for callers (and tests) that want to distinguish the two.
func IsPermalink(u *url.URL) bool {
	return u != nil && permalinkRE.MatchString(u.Path)
}
