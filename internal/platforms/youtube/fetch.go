// Package youtube fetches a YouTube video's metadata and transcript
// using github.com/kkdai/youtube/v2 (no auth needed) and, optionally,
// its top-level comments via the YouTube Data API v3 when
// YOUTUBE_API_KEY is set in the environment.
//
// The transcript is rendered into the item's Content as plain text,
// preceded by the video description; timestamped segments are kept in
// item.Extra["transcript"] for callers that want to render them
// differently (skim/jump-to-time).
package youtube

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	yt "github.com/kkdai/youtube/v2"

	"github.com/jedi4ever/socialfetch/internal/core"
)

type Fetcher struct {
	// Client is the kkdai/youtube/v2 client used for metadata and
	// transcripts. Tests override it; callers normally don't.
	Client *yt.Client

	// CommentsBase overrides the YouTube Data API v3 base URL. Tests
	// point this at a httptest server.
	CommentsBase string

	// APIKey overrides $YOUTUBE_API_KEY. Tests use it.
	APIKey string

	// PreferredLang is the caption language to try first; falls back
	// to whatever the video offers.
	PreferredLang string
}

func New() *Fetcher {
	return &Fetcher{
		Client:        &yt.Client{},
		CommentsBase:  "https://www.googleapis.com/youtube/v3",
		PreferredLang: "en",
	}
}

func (Fetcher) Name() string { return "youtube" }

// videoIDRE matches an 11-char YouTube ID; used to validate IDs we
// extract from URL paths/queries before passing them downstream.
var videoIDRE = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)

func (Fetcher) Match(u *url.URL) bool {
	if u == nil {
		return false
	}
	host := strings.TrimPrefix(strings.ToLower(u.Host), "www.")
	host = strings.TrimPrefix(host, "m.")
	switch host {
	case "youtube.com", "music.youtube.com":
		return extractIDFromURL(u) != ""
	case "youtu.be":
		return extractIDFromURL(u) != ""
	}
	return false
}

// extractIDFromURL pulls the 11-char video ID out of any of the
// recognized URL shapes:
//
//	youtube.com/watch?v=ID
//	youtu.be/ID
//	youtube.com/shorts/ID
//	youtube.com/live/ID
//	youtube.com/embed/ID
func extractIDFromURL(u *url.URL) string {
	if u == nil {
		return ""
	}
	host := strings.TrimPrefix(strings.ToLower(u.Host), "www.")
	host = strings.TrimPrefix(host, "m.")

	if host == "youtu.be" {
		id := strings.Trim(u.Path, "/")
		if videoIDRE.MatchString(id) {
			return id
		}
		return ""
	}

	if v := u.Query().Get("v"); videoIDRE.MatchString(v) {
		return v
	}
	for _, prefix := range []string{"/shorts/", "/live/", "/embed/"} {
		if i := strings.Index(u.Path, prefix); i >= 0 {
			rest := u.Path[i+len(prefix):]
			if j := strings.IndexByte(rest, '/'); j >= 0 {
				rest = rest[:j]
			}
			if videoIDRE.MatchString(rest) {
				return rest
			}
		}
	}
	return ""
}

func (f *Fetcher) Fetch(ctx context.Context, raw string, opts core.Options) (*core.Item, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("youtube: invalid url: %w", err)
	}
	id := extractIDFromURL(u)
	if id == "" {
		return nil, fmt.Errorf("youtube: no video id in %q", raw)
	}
	ctx = core.WithAudit(ctx, opts.Audit)

	opts.Audit.Logf("youtube: fetching metadata for %s", id)
	video, err := f.Client.GetVideoContext(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("youtube: get video: %w", err)
	}

	transcriptText, segments := f.fetchTranscript(ctx, video, opts)

	body := strings.TrimSpace(video.Description)
	if transcriptText != "" {
		if body != "" {
			body += "\n\n## Transcript\n\n"
		}
		body += transcriptText
	}

	canonical := "https://www.youtube.com/watch?v=" + video.ID
	published := video.PublishDate

	item := &core.Item{
		Source:      "youtube",
		Kind:        "video",
		URL:         canonical,
		CanonicalID: video.ID,
		Title:       video.Title,
		Author:      video.Author,
		AuthorURL:   "https://www.youtube.com/" + nonEmpty(video.ChannelHandle, "channel/"+video.ChannelID),
		Published:   timePtr(published),
		Content:     body,
		Score:       int(video.Views),
		Tags:        nil,
		FetchedAt:   time.Now().UTC(),
		Extra: map[string]any{
			"channel_id":     video.ChannelID,
			"channel_handle": video.ChannelHandle,
			"duration":       video.Duration.String(),
			"duration_secs":  int(video.Duration.Seconds()),
			"view_count":     video.Views,
		},
	}
	if len(segments) > 0 {
		item.Extra["transcript"] = segments
	}
	if len(video.Thumbnails) > 0 {
		// Pick the highest-resolution thumbnail.
		best := video.Thumbnails[0]
		for _, t := range video.Thumbnails {
			if t.Width > best.Width {
				best = t
			}
		}
		item.Media = []core.Media{{URL: best.URL, Type: "image", Alt: video.Title}}
	}

	if opts.IncludeComments {
		key := f.APIKey
		if key == "" {
			key = os.Getenv("YOUTUBE_API_KEY")
		}
		if key == "" {
			opts.Audit.Logf("youtube: skipping comments (set YOUTUBE_API_KEY to fetch them)")
		} else {
			comments, err := f.fetchComments(ctx, video.ID, key, opts)
			if err != nil {
				opts.Audit.Logf("youtube: comments fetch failed: %v", err)
			} else {
				item.Comments = comments
				item.Extra["comment_count"] = countComments(comments)
			}
		}
	}

	return item, nil
}

// segment is one transcript line in a JSON-friendly shape.
type segment struct {
	StartMs    int    `json:"start_ms"`
	DurationMs int    `json:"duration_ms"`
	Offset     string `json:"offset"`
	Text       string `json:"text"`
}

// fetchTranscript returns a plain-text rendering of the transcript
// plus the structured segment list. Provider selection is controlled
// by the YOUTUBE_TRANSCRIPT_PROVIDER env var:
//
//	auto       (default) — try yt-dlp if installed, else innertube, else kkdai
//	ytdlp      — yt-dlp only (most reliable; needs the binary on PATH)
//	innertube  — pure-Go scrape via youtubei/v1/get_transcript
//	kkdai      — kkdai/youtube/v2's caption-track endpoint
//
// Each provider returns an empty result + an audit-log line on failure
// so we can fall through to the next without surfacing an error to the
// caller (a video without a transcript shouldn't fail the whole fetch).
func (f *Fetcher) fetchTranscript(ctx context.Context, video *yt.Video, opts core.Options) (string, []segment) {
	provider := strings.ToLower(strings.TrimSpace(os.Getenv("YOUTUBE_TRANSCRIPT_PROVIDER")))
	if provider == "" {
		provider = "auto"
	}

	tryProvider := func(name string) ([]segment, bool) {
		switch name {
		case "ytdlp":
			if !ytDlpAvailable() {
				opts.Audit.Logf("youtube: yt-dlp not installed; skipping")
				return nil, false
			}
			segs, err := fetchTranscriptYtDlp(ctx, video.ID, f.PreferredLang, opts.Audit)
			if err != nil {
				opts.Audit.Logf("youtube: yt-dlp transcript failed: %v", err)
				return nil, false
			}
			return segs, true
		case "innertube":
			segs, err := fetchTranscriptInnertube(ctx, video.ID, opts.Audit)
			if err != nil {
				opts.Audit.Logf("youtube: innertube transcript failed: %v", err)
				return nil, false
			}
			return segs, true
		case "kkdai":
			segs := f.fetchTranscriptKkdai(ctx, video, opts)
			return segs, len(segs) > 0
		}
		return nil, false
	}

	order := []string{provider}
	if provider == "auto" {
		order = []string{"ytdlp", "innertube", "kkdai"}
	}
	for _, p := range order {
		if segs, ok := tryProvider(p); ok {
			opts.Audit.Logf("youtube: transcript via %s (%d segments)", p, len(segs))
			return segmentsToText(segs), segs
		}
	}
	return "", nil
}

// fetchTranscriptKkdai uses kkdai/youtube/v2's caption-track endpoint.
// Kept separate so the auto-provider above can call it as one option
// among several.
func (f *Fetcher) fetchTranscriptKkdai(ctx context.Context, video *yt.Video, opts core.Options) []segment {
	if len(video.CaptionTracks) == 0 {
		opts.Audit.Logf("youtube: no caption tracks on this video")
		return nil
	}
	tries := []string{f.PreferredLang}
	for _, c := range video.CaptionTracks {
		tries = append(tries, c.LanguageCode)
	}
	tried := map[string]bool{}
	for _, lang := range tries {
		if lang == "" || tried[lang] {
			continue
		}
		tried[lang] = true
		opts.Audit.Logf("youtube: kkdai trying lang %q", lang)
		tr, err := f.Client.GetTranscriptCtx(ctx, video, lang)
		if err != nil {
			opts.Audit.Logf("youtube: kkdai %q failed: %v", lang, err)
			continue
		}
		if len(tr) == 0 {
			continue
		}
		segs := make([]segment, 0, len(tr))
		for _, s := range tr {
			line := strings.TrimSpace(s.Text)
			if line == "" {
				continue
			}
			segs = append(segs, segment{
				StartMs:    s.StartMs,
				DurationMs: s.Duration,
				Offset:     s.OffsetText,
				Text:       line,
			})
		}
		return segs
	}
	return nil
}

func segmentsToText(segs []segment) string {
	var b strings.Builder
	for _, s := range segs {
		t := strings.TrimSpace(s.Text)
		if t == "" {
			continue
		}
		b.WriteString(t)
		b.WriteByte('\n')
	}
	return strings.TrimSpace(b.String())
}

func nonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func timePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	u := t.UTC()
	return &u
}

func countComments(cs []core.Comment) int {
	n := len(cs)
	for _, c := range cs {
		n += countComments(c.Replies)
	}
	return n
}

// videoIDFromAny lets callers pass either a URL or a bare 11-char ID.
// Currently unused; kept around for the cmd-tests we'll add later.
var _ = errors.New // keep errors import if we add wrapping later
