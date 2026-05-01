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
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/patrickdebois/social-skills/internal/core"
	"github.com/patrickdebois/social-skills/internal/xauth"
)

const defaultBaseURL = "https://cdn.syndication.twimg.com"

// Fetcher pulls a tweet by URL. When X_API_KEY+X_API_SECRET are set in
// the environment we use the official v2 API (gives us long-form
// note_tweet content); otherwise we fall back to the public syndication
// endpoint, which is auth-free but truncates long tweets.
type Fetcher struct {
	BaseURL    string // syndication base
	APIBaseURL string // v2 base, e.g. https://api.twitter.com/2
	// Creds optionally overrides $X_API_KEY/$X_API_SECRET. Tests use it.
	Creds xauth.Credentials
}

func New() *Fetcher {
	return &Fetcher{
		BaseURL:    defaultBaseURL,
		APIBaseURL: "https://api.twitter.com/2",
	}
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

	// Prefer the official v2 API when credentials are available — gives us
	// note_tweet for long-form posts. Fall back to the public syndication
	// endpoint when not.
	if creds, ok := f.creds(); ok {
		opts.Audit.Logf("twitter: using v2 API for %s", id)
		if item, err := f.fetchViaAPI(ctx, id, creds); err == nil {
			return item, nil
		} else {
			opts.Audit.Logf("twitter: v2 API failed (%v), falling back to syndication", err)
		}
	}

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

// creds picks an explicit Creds field over $X_API_KEY/$X_API_SECRET so
// tests can wire credentials without poking the environment.
func (f *Fetcher) creds() (xauth.Credentials, bool) {
	if f.Creds.Key != "" && f.Creds.Secret != "" {
		return f.Creds, true
	}
	return xauth.FromEnv()
}

// fetchViaAPI calls X's v2 single-tweet endpoint. Long-form posts use
// note_tweet; we always read it when available since the regular `text`
// is a 280-char stub.
func (f *Fetcher) fetchViaAPI(ctx context.Context, id string, creds xauth.Credentials) (*core.Item, error) {
	bearer, err := xauth.BearerToken(ctx, creds)
	if err != nil {
		return nil, err
	}

	q := url.Values{
		"expansions":   {"author_id,attachments.media_keys"},
		"tweet.fields": {"created_at,public_metrics,lang,note_tweet,entities"},
		"user.fields":  {"username,name"},
		"media.fields": {"url,type,variants"},
	}
	endpoint := fmt.Sprintf("%s/tweets/%s?%s", f.APIBaseURL, id, q.Encode())

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("User-Agent", core.UserAgent)

	resp, err := core.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("v2 tweets/%s: HTTP %d", id, resp.StatusCode)
	}

	var body apiTweet
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	if body.Data.ID == "" {
		return nil, fmt.Errorf("v2: empty data for %s", id)
	}

	user := struct {
		Name, Username string
	}{}
	for _, u := range body.Includes.Users {
		if u.ID == body.Data.AuthorID {
			user.Name = u.Name
			user.Username = u.Username
			break
		}
	}

	text := body.Data.Text
	if body.Data.NoteTweet != nil && body.Data.NoteTweet.Text != "" {
		text = body.Data.NoteTweet.Text
	}
	for _, u := range body.Data.Entities.URLs {
		if u.URL != "" && u.ExpandedURL != "" {
			text = strings.ReplaceAll(text, u.URL, u.ExpandedURL)
		}
	}

	media := []core.Media{}
	for _, m := range body.Includes.Media {
		switch m.Type {
		case "photo":
			if m.URL != "" {
				media = append(media, core.Media{URL: m.URL, Type: "image"})
			}
		case "video", "animated_gif":
			best := pickV2Video(m.Variants)
			if best != "" {
				media = append(media, core.Media{URL: best, Type: "video"})
			}
		}
	}

	published := parseTwitterTime(body.Data.CreatedAt)
	return &core.Item{
		Source:      "twitter",
		Kind:        "tweet",
		URL:         fmt.Sprintf("https://x.com/%s/status/%s", user.Username, body.Data.ID),
		CanonicalID: body.Data.ID,
		Title:       firstLine(text, 80),
		Author:      user.Name,
		AuthorURL:   "https://x.com/" + user.Username,
		Published:   published,
		Content:     text,
		Score:       body.Data.PublicMetrics.Likes,
		Media:       media,
		FetchedAt:   time.Now().UTC(),
		Extra: map[string]any{
			"screen_name":    user.Username,
			"reply_count":    body.Data.PublicMetrics.Replies,
			"favorite_count": body.Data.PublicMetrics.Likes,
			"retweet_count":  body.Data.PublicMetrics.Reposts,
			"lang":           body.Data.Lang,
			"via":            "v2_api",
		},
	}, nil
}

// apiTweet models the slice of X v2's payload we use.
type apiTweet struct {
	Data struct {
		ID            string `json:"id"`
		Text          string `json:"text"`
		AuthorID      string `json:"author_id"`
		CreatedAt     string `json:"created_at"`
		Lang          string `json:"lang"`
		PublicMetrics struct {
			Likes   int `json:"like_count"`
			Reposts int `json:"retweet_count"`
			Replies int `json:"reply_count"`
		} `json:"public_metrics"`
		NoteTweet *struct {
			Text string `json:"text"`
		} `json:"note_tweet"`
		Entities struct {
			URLs []struct {
				URL         string `json:"url"`
				ExpandedURL string `json:"expanded_url"`
			} `json:"urls"`
		} `json:"entities"`
	} `json:"data"`
	Includes struct {
		Users []struct {
			ID       string `json:"id"`
			Name     string `json:"name"`
			Username string `json:"username"`
		} `json:"users"`
		Media []struct {
			Key      string `json:"media_key"`
			Type     string `json:"type"`
			URL      string `json:"url"`
			Variants []struct {
				Bitrate     int    `json:"bit_rate"`
				ContentType string `json:"content_type"`
				URL         string `json:"url"`
			} `json:"variants"`
		} `json:"media"`
	} `json:"includes"`
}

// pickV2Video returns the highest-bitrate MP4 variant from v2's media list.
func pickV2Video(variants []struct {
	Bitrate     int    `json:"bit_rate"`
	ContentType string `json:"content_type"`
	URL         string `json:"url"`
}) string {
	best := ""
	bestBR := -1
	for _, v := range variants {
		if v.ContentType != "video/mp4" {
			continue
		}
		if v.Bitrate > bestBR {
			bestBR = v.Bitrate
			best = v.URL
		}
	}
	return best
}

