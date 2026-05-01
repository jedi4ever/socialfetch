package youtube

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/patrickdebois/social-skills/internal/core"
)

// commentThreadsResp models the slice of YouTube Data API v3
// /commentThreads we read. Per-call cost is 1 quota unit; we paginate
// until MaxComments is satisfied or the API runs out of pages.
type commentThreadsResp struct {
	NextPageToken string `json:"nextPageToken"`
	Items         []struct {
		Snippet struct {
			TopLevelComment struct {
				ID      string         `json:"id"`
				Snippet commentSnippet `json:"snippet"`
			} `json:"topLevelComment"`
			TotalReplyCount int `json:"totalReplyCount"`
		} `json:"snippet"`
		Replies *struct {
			Comments []struct {
				ID      string         `json:"id"`
				Snippet commentSnippet `json:"snippet"`
			} `json:"comments"`
		} `json:"replies,omitempty"`
	} `json:"items"`
}

type commentSnippet struct {
	AuthorDisplayName     string `json:"authorDisplayName"`
	AuthorChannelURL      string `json:"authorChannelUrl"`
	TextDisplay           string `json:"textDisplay"`
	TextOriginal          string `json:"textOriginal"`
	LikeCount             int    `json:"likeCount"`
	PublishedAt           string `json:"publishedAt"`
	UpdatedAt             string `json:"updatedAt"`
}

// pageSize is the YouTube API per-call limit. Setting it to 100 keeps
// pagination minimal — we usually return after one or two calls even
// for popular videos.
const pageSize = 100

// hardPageCap protects against runaway pagination on viral videos.
const hardPageCap = 20

// fetchComments paginates /commentThreads, building a tree per
// top-level item. Replies returned inline by the API are attached
// directly; we don't issue separate /comments calls for deeper
// threads (YouTube's reply nesting is single-level by API contract).
func (f *Fetcher) fetchComments(ctx context.Context, videoID, key string, opts core.Options) ([]core.Comment, error) {
	var out []core.Comment
	pageToken := ""
	pages := 0
	want := opts.MaxComments
	if want <= 0 {
		want = -1 // unlimited
	}

	for {
		pages++
		if pages > hardPageCap {
			opts.Audit.Logf("youtube: comment pagination cap (%d pages) hit", hardPageCap)
			break
		}

		page, err := f.fetchCommentPage(ctx, videoID, key, pageToken)
		if err != nil {
			return nil, err
		}

		for _, t := range page.Items {
			c := buildComment(t.Snippet.TopLevelComment.ID,
				t.Snippet.TopLevelComment.Snippet, 0)
			if t.Replies != nil {
				for _, r := range t.Replies.Comments {
					c.Replies = append(c.Replies, buildComment(r.ID, r.Snippet, 1))
				}
			}
			out = append(out, c)
			if want > 0 && countComments(out) >= want {
				break
			}
		}
		opts.Audit.Logf("youtube: fetched %d comment threads (running total %d)",
			len(page.Items), countComments(out))

		if page.NextPageToken == "" {
			break
		}
		if want > 0 && countComments(out) >= want {
			break
		}
		pageToken = page.NextPageToken
	}
	return out, nil
}

func (f *Fetcher) fetchCommentPage(ctx context.Context, videoID, key, pageToken string) (*commentThreadsResp, error) {
	q := url.Values{
		"part":       {"snippet,replies"},
		"videoId":    {videoID},
		"maxResults": {strconv.Itoa(pageSize)},
		"order":      {"relevance"},
		"key":        {key},
	}
	if pageToken != "" {
		q.Set("pageToken", pageToken)
	}
	endpoint := fmt.Sprintf("%s/commentThreads?%s", f.CommentsBase, q.Encode())

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", core.UserAgent)

	resp, err := core.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("commentThreads: HTTP 403 — comments may be disabled, or the API key is restricted/quota-exhausted")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("commentThreads: HTTP %d", resp.StatusCode)
	}
	var page commentThreadsResp
	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
		return nil, fmt.Errorf("commentThreads decode: %w", err)
	}
	return &page, nil
}

func buildComment(id string, s commentSnippet, depth int) core.Comment {
	c := core.Comment{
		ID:     id,
		Author: s.AuthorDisplayName,
		Body:   s.TextOriginal, // textOriginal has no entity-encoded HTML
		Score:  s.LikeCount,
		Depth:  depth,
	}
	if t := parseRFC3339(s.PublishedAt); t != nil {
		c.Published = t
	}
	return c
}

func parseRFC3339(s string) *time.Time {
	if s == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return nil
	}
	u := t.UTC()
	return &u
}
