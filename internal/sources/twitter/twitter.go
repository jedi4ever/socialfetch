// Package twitter fetches a single tweet using the public syndication
// endpoint at cdn.syndication.twimg.com. No authentication needed — this
// is the same endpoint Twitter's own embed widgets use.
//
// Note: the syndication endpoint requires a "token" query param computed
// from the tweet ID. The algorithm is well-known and stable: take the
// numeric ID times 4096, divide by 1e15, and strip trailing zeros.
package twitter

import (
	"context"
	"fmt"
	"math"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/patrickdebois/social-skills/internal/core"
)

const defaultBaseURL = "https://cdn.syndication.twimg.com"

// Fetcher pulls a tweet by URL.
type Fetcher struct {
	BaseURL string
}

func New() *Fetcher {
	return &Fetcher{BaseURL: defaultBaseURL}
}

func (Fetcher) Name() string { return "twitter" }

func (Fetcher) Match(u *url.URL) bool {
	if u == nil {
		return false
	}
	host := strings.TrimPrefix(u.Host, "www.")
	if host != "twitter.com" && host != "x.com" && host != "mobile.twitter.com" {
		return false
	}
	return tweetIDRE.MatchString(u.Path)
}

var tweetIDRE = regexp.MustCompile(`/status(?:es)?/(\d+)`)

func extractID(raw string) (string, error) {
	m := tweetIDRE.FindStringSubmatch(raw)
	if len(m) < 2 {
		return "", fmt.Errorf("twitter: no status id in %q", raw)
	}
	return m[1], nil
}

// syndicationToken implements the token derivation Twitter's embed widget
// uses:
//
//	((Number(id) / 1e15) * 4096).toString(36).replace(/(0+|\.)/g, '')
//
// We mimic JavaScript's Number#toString(36) for the floating-point result,
// then strip every run of zeros and the decimal point.
func syndicationToken(id string) string {
	n, err := strconv.ParseFloat(id, 64)
	if err != nil {
		return ""
	}
	v := (n / 1e15) * 4096
	return stripZerosAndDot(jsToBase36(v))
}

// jsToBase36 mimics JS's Number#toString(36) for non-negative finite values.
// Integer part uses big-endian base-36; fractional part is computed by
// repeated multiplication, capped at 16 digits which is enough precision
// to match V8's output for the values we hand it.
func jsToBase36(f float64) string {
	if math.IsNaN(f) || f < 0 {
		return "0"
	}
	intPart := math.Floor(f)
	frac := f - intPart

	intStr := strconv.FormatInt(int64(intPart), 36)
	if frac == 0 {
		return intStr
	}

	var b strings.Builder
	b.WriteString(intStr)
	b.WriteByte('.')
	for i := 0; i < 16 && frac > 0; i++ {
		frac *= 36
		d := int(math.Floor(frac))
		if d < 10 {
			b.WriteByte(byte('0' + d))
		} else {
			b.WriteByte(byte('a' + d - 10))
		}
		frac -= float64(d)
	}
	return b.String()
}

func stripZerosAndDot(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '.' || c == '0' {
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}

// tweetResponse models the subset of fields we read from the syndication
// payload. The endpoint returns a lot more; we ignore it.
type tweetResponse struct {
	ID       string `json:"id_str"`
	Text     string `json:"text"`
	Created  string `json:"created_at"`
	Lang     string `json:"lang"`
	User     struct {
		Name       string `json:"name"`
		ScreenName string `json:"screen_name"`
		ProfileURL string `json:"profile_image_url_https"`
	} `json:"user"`
	Photos []struct {
		URL string `json:"url"`
	} `json:"photos"`
	Video *struct {
		Variants []struct {
			Type    string `json:"type"`
			Src     string `json:"src"`
			Bitrate int    `json:"bitrate"`
		} `json:"variants"`
	} `json:"video"`
	Entities struct {
		URLs []struct {
			URL         string `json:"url"`
			ExpandedURL string `json:"expanded_url"`
		} `json:"urls"`
	} `json:"entities"`
	FavoriteCount int `json:"favorite_count"`
	ReplyCount    int `json:"conversation_count"`
}

func (f *Fetcher) Fetch(ctx context.Context, raw string, opts core.Options) (*core.Item, error) {
	id, err := extractID(raw)
	if err != nil {
		return nil, err
	}
	ctx = core.WithAudit(ctx, opts.Audit)

	// The syndication endpoint requires both an id and a token. If our
	// token derivation is off, the endpoint returns 404 — that's why we
	// also send a small random "client" param (matches the official embed
	// widget behavior) for cache busting in tests.
	tok := syndicationToken(id)
	endpoint := fmt.Sprintf("%s/tweet-result?id=%s&token=%s&lang=en", f.BaseURL, id, tok)

	var tw tweetResponse
	if err := core.GetJSON(ctx, endpoint, &tw); err != nil {
		return nil, fmt.Errorf("twitter: %w", err)
	}
	if tw.ID == "" {
		return nil, fmt.Errorf("twitter: empty response for id %s", id)
	}

	published := parseTwitterTime(tw.Created)
	media := []core.Media{}
	for _, p := range tw.Photos {
		media = append(media, core.Media{URL: p.URL, Type: "image"})
	}
	if tw.Video != nil {
		best := pickVideo(tw.Video.Variants)
		if best != "" {
			media = append(media, core.Media{URL: best, Type: "video"})
		}
	}

	// Replace t.co URLs in the body with the expanded ones for readability.
	body := tw.Text
	for _, u := range tw.Entities.URLs {
		if u.URL != "" && u.ExpandedURL != "" {
			body = strings.ReplaceAll(body, u.URL, u.ExpandedURL)
		}
	}

	item := &core.Item{
		Source:      "twitter",
		Kind:        "tweet",
		URL:         fmt.Sprintf("https://x.com/%s/status/%s", tw.User.ScreenName, tw.ID),
		CanonicalID: tw.ID,
		Title:       firstLine(body, 80),
		Author:      tw.User.Name,
		AuthorURL:   "https://x.com/" + tw.User.ScreenName,
		Published:   published,
		Content:     body,
		Score:       tw.FavoriteCount,
		Media:       media,
		FetchedAt:   time.Now().UTC(),
		Extra: map[string]any{
			"screen_name":   tw.User.ScreenName,
			"reply_count":   tw.ReplyCount,
			"favorite_count": tw.FavoriteCount,
			"lang":          tw.Lang,
		},
	}
	return item, nil
}

// pickVideo returns the highest-bitrate MP4 variant URL.
func pickVideo(variants []struct {
	Type    string `json:"type"`
	Src     string `json:"src"`
	Bitrate int    `json:"bitrate"`
}) string {
	best := ""
	bestBR := -1
	for _, v := range variants {
		if v.Type != "video/mp4" {
			continue
		}
		if v.Bitrate > bestBR {
			bestBR = v.Bitrate
			best = v.Src
		}
	}
	return best
}

func parseTwitterTime(s string) *time.Time {
	if s == "" {
		return nil
	}
	for _, layout := range []string{
		time.RFC3339,
		"Mon Jan 02 15:04:05 -0700 2006", // X's classic format
	} {
		if t, err := time.Parse(layout, s); err == nil {
			u := t.UTC()
			return &u
		}
	}
	return nil
}

func firstLine(s string, max int) string {
	if i := strings.IndexAny(s, "\n\r"); i >= 0 {
		s = s[:i]
	}
	if len(s) > max {
		s = s[:max-1] + "…"
	}
	return s
}

