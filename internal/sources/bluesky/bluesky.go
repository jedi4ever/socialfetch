// Package bluesky fetches a single post (and its reply thread) from
// the Bluesky public AppView. No auth required — the AppView at
// public.api.bsky.app exposes the same XRPC methods authenticated
// clients use, scoped to public-readable content.
//
// Flow:
//
//  1. Parse the URL into (handle-or-did, rkey).
//  2. If the handle isn't already a DID, call
//     com.atproto.identity.resolveHandle to get one.
//  3. Build an AT URI: at://<did>/app.bsky.feed.post/<rkey>.
//  4. Call app.bsky.feed.getPostThread with that URI and walk the
//     reply tree into core.Comment.
package bluesky

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

// AppViewBase is the public AppView XRPC base URL. Callable without
// auth for everything we need; the equivalent authenticated entrypoint
// is bsky.social, but that needs an OAuth or app-password session.
const AppViewBase = "https://public.api.bsky.app/xrpc"

type Fetcher struct {
	BaseURL string // overrides AppViewBase for tests
}

func New() *Fetcher { return &Fetcher{BaseURL: AppViewBase} }

func (Fetcher) Name() string { return "bluesky" }

// postURLRE matches the post URL shape used by bsky.app.
var postURLRE = regexp.MustCompile(`^/profile/([^/]+)/post/([^/?#]+)`)

func (Fetcher) Match(u *url.URL) bool {
	if u == nil {
		return false
	}
	host := strings.TrimPrefix(strings.ToLower(u.Host), "www.")
	if host != "bsky.app" {
		return false
	}
	return postURLRE.MatchString(u.Path)
}

func extractIDs(raw string) (handle, rkey string, err error) {
	u, parseErr := url.Parse(raw)
	if parseErr != nil {
		return "", "", parseErr
	}
	m := postURLRE.FindStringSubmatch(u.Path)
	if len(m) < 3 {
		return "", "", fmt.Errorf("no post id in %q", raw)
	}
	return m[1], m[2], nil
}

func (f *Fetcher) base() string {
	if f.BaseURL != "" {
		return f.BaseURL
	}
	return AppViewBase
}

// Fetch builds the post + thread.
func (f *Fetcher) Fetch(ctx context.Context, raw string, opts core.Options) (*core.Item, error) {
	handle, rkey, err := extractIDs(raw)
	if err != nil {
		return nil, fmt.Errorf("bluesky: %w", err)
	}
	ctx = core.WithAudit(ctx, opts.Audit)

	did := handle
	if !strings.HasPrefix(did, "did:") {
		opts.Audit.Logf("bluesky: resolving handle %s", handle)
		did, err = f.resolveHandle(ctx, handle)
		if err != nil {
			return nil, fmt.Errorf("bluesky: resolve %s: %w", handle, err)
		}
	}

	atURI := fmt.Sprintf("at://%s/app.bsky.feed.post/%s", did, rkey)
	opts.Audit.Logf("bluesky: getPostThread %s", atURI)
	thread, err := f.getPostThread(ctx, atURI, opts)
	if err != nil {
		return nil, fmt.Errorf("bluesky: thread: %w", err)
	}
	if thread == nil || thread.Post == nil {
		return nil, fmt.Errorf("bluesky: empty thread for %s", atURI)
	}

	root := thread.Post
	published := parseTime(root.Record.CreatedAt)

	item := &core.Item{
		Source:      "bluesky",
		Kind:        "post",
		URL:         fmt.Sprintf("https://bsky.app/profile/%s/post/%s", root.Author.Handle, rkey),
		CanonicalID: rkey,
		Title:       firstLine(root.Record.Text, 80),
		Author:      pickAuthor(root.Author),
		AuthorURL:   "https://bsky.app/profile/" + root.Author.Handle,
		Published:   published,
		Content:     strings.TrimSpace(root.Record.Text),
		Score:       root.LikeCount,
		FetchedAt:   time.Now().UTC(),
		Extra: map[string]any{
			"did":           root.Author.DID,
			"handle":        root.Author.Handle,
			"like_count":    root.LikeCount,
			"reply_count":   root.ReplyCount,
			"repost_count":  root.RepostCount,
			"quote_count":   root.QuoteCount,
		},
	}
	for _, m := range extractMedia(root) {
		item.Media = append(item.Media, m)
	}

	if opts.IncludeComments && len(thread.Replies) > 0 {
		item.Comments = walkReplies(thread.Replies, 0, opts.MaxComments)
	}
	return item, nil
}

// ----- Wire types --------------------------------------------------------

type author struct {
	DID         string `json:"did"`
	Handle      string `json:"handle"`
	DisplayName string `json:"displayName"`
}

type postRecord struct {
	Text      string `json:"text"`
	CreatedAt string `json:"createdAt"`
}

type post struct {
	URI         string     `json:"uri"`
	CID         string     `json:"cid"`
	Author      author     `json:"author"`
	Record      postRecord `json:"record"`
	LikeCount   int        `json:"likeCount"`
	ReplyCount  int        `json:"replyCount"`
	RepostCount int        `json:"repostCount"`
	QuoteCount  int        `json:"quoteCount"`
	Embed       *embed     `json:"embed,omitempty"`
}

type embed struct {
	Type    string  `json:"$type"`
	Images  []embedImg `json:"images,omitempty"`
	External *embedExternal `json:"external,omitempty"`
}

type embedImg struct {
	Thumb    string `json:"thumb"`
	Fullsize string `json:"fullsize"`
	Alt      string `json:"alt"`
}

type embedExternal struct {
	URI         string `json:"uri"`
	Title       string `json:"title"`
	Description string `json:"description"`
}

type threadView struct {
	Post    *post         `json:"post"`
	Replies []threadView  `json:"replies,omitempty"`
}

type getPostThreadResp struct {
	Thread threadView `json:"thread"`
}

// ----- HTTP helpers ------------------------------------------------------

func (f *Fetcher) resolveHandle(ctx context.Context, handle string) (string, error) {
	q := url.Values{"handle": {handle}}
	endpoint := f.base() + "/com.atproto.identity.resolveHandle?" + q.Encode()
	var out struct {
		DID string `json:"did"`
	}
	if err := core.GetJSON(ctx, endpoint, &out); err != nil {
		return "", err
	}
	if out.DID == "" {
		return "", fmt.Errorf("no did in resolve response")
	}
	return out.DID, nil
}

func (f *Fetcher) getPostThread(ctx context.Context, atURI string, opts core.Options) (*threadView, error) {
	q := url.Values{
		"uri":           {atURI},
		"depth":         {"6"},
		"parentHeight":  {"0"},
	}
	endpoint := f.base() + "/app.bsky.feed.getPostThread?" + q.Encode()
	var out getPostThreadResp
	if err := core.GetJSON(ctx, endpoint, &out); err != nil {
		return nil, err
	}
	return &out.Thread, nil
}

// ----- Tree + helpers ----------------------------------------------------

// walkReplies converts the recursive threadView tree into core.Comment.
// Depth is propagated; when max > 0, traversal stops once we've
// accumulated max comments (depth-first preorder, so we keep nearer
// replies over deep tail threads).
func walkReplies(replies []threadView, depth, max int) []core.Comment {
	remaining := max
	return collectReplies(replies, depth, &remaining, max)
}

func collectReplies(rs []threadView, depth int, remaining *int, max int) []core.Comment {
	var out []core.Comment
	for _, r := range rs {
		if max > 0 && *remaining == 0 {
			return out
		}
		if r.Post == nil {
			continue
		}
		if max > 0 {
			*remaining--
		}
		out = append(out, core.Comment{
			ID:        r.Post.URI,
			Author:    pickAuthor(r.Post.Author),
			Body:      strings.TrimSpace(r.Post.Record.Text),
			Score:     r.Post.LikeCount,
			Published: parseTime(r.Post.Record.CreatedAt),
			Depth:     depth,
			Replies:   collectReplies(r.Replies, depth+1, remaining, max),
		})
	}
	return out
}

func pickAuthor(a author) string {
	if a.DisplayName != "" {
		return fmt.Sprintf("%s (@%s)", a.DisplayName, a.Handle)
	}
	if a.Handle != "" {
		return "@" + a.Handle
	}
	return a.DID
}

func parseTime(s string) *time.Time {
	if s == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		t, err = time.Parse(time.RFC3339, s)
	}
	if err != nil {
		return nil
	}
	u := t.UTC()
	return &u
}

func firstLine(s string, max int) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\n\r"); i >= 0 {
		s = s[:i]
	}
	if len(s) > max {
		s = s[:max-1] + "…"
	}
	return s
}

func extractMedia(p *post) []core.Media {
	if p == nil || p.Embed == nil {
		return nil
	}
	out := []core.Media{}
	for _, img := range p.Embed.Images {
		alt := img.Alt
		url := img.Fullsize
		if url == "" {
			url = img.Thumb
		}
		if url == "" {
			continue
		}
		out = append(out, core.Media{URL: url, Type: "image", Alt: alt})
	}
	return out
}

// Force-include json.Marshal so go-mod-tidy keeps the import even
// when we add wire types but no explicit decoder yet.
var _ = json.Marshal
