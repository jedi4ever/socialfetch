package youtube

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/jedi4ever/socialfetch/internal/core"
)

// InnerTube is YouTube's private internal API — the same one
// youtube.com itself calls. We use it for transcripts because the
// public Data API v3's captions.download endpoint requires OAuth as
// the channel owner (no good for arbitrary public videos). The kkdai
// library tries the legacy public timedtext URL but YouTube has been
// returning 400 on that path for many videos in 2026.
//
// Caveats baked into using this:
//   - Not officially documented or sanctioned by YouTube.
//   - Endpoints, response shapes, and the public client key can change
//     without notice; we hunt for `transcriptSegmentRenderer` anywhere
//     in the JSON tree to survive minor schema drift.
//   - Some videos (live, members-only, age-restricted, region-locked,
//     or those whose channel disabled transcripts) return no segments
//     even when the call succeeds.
const (
	// innertubeBase is the InnerTube get_transcript endpoint.
	innertubeBase = "https://www.youtube.com/youtubei/v1/get_transcript"

	// innertubeKey is the public WEB-client key embedded in the
	// youtube.com page source. It's not a secret — every YouTube web
	// page reveals it on view-source. Documented here so anyone can
	// rotate it when YouTube does.
	innertubeKey = "AIzaSyAO_FJ2SlqU8Q4STEHLGCilw_Y9_11qcW8"

	// clientName / clientVersion impersonate a recent YouTube WEB
	// session. Bump clientVersion if YouTube starts gating older ones.
	innertubeClientName    = "WEB"
	innertubeClientVersion = "2.20240826.01.00"

	// innertubeUA: the internal endpoint occasionally rejects empty or
	// non-browser User-Agents. A vanilla desktop UA keeps it happy.
	innertubeUA = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) " +
		"AppleWebKit/537.36 (KHTML, like Gecko) " +
		"Chrome/126.0.0.0 Safari/537.36"
)

// fetchTranscriptInnertube returns timed segments for a video by
// calling InnerTube's get_transcript endpoint.
//
// We scrape the watch page first to extract the server-generated
// `getTranscriptEndpoint.params` continuation token. Without that,
// YouTube returns FAILED_PRECONDITION (a hand-rolled minimal protobuf
// of just the videoId is rejected by their validators). Two HTTP
// hops, but matches yt-dlp's known-working approach.
func fetchTranscriptInnertube(ctx context.Context, videoID string, audit *core.AuditLogger) ([]segment, error) {
	params, err := scrapeTranscriptParams(ctx, videoID, audit)
	if err != nil {
		return nil, err
	}

	body, err := json.Marshal(map[string]any{
		"context": map[string]any{
			"client": map[string]any{
				"clientName":    innertubeClientName,
				"clientVersion": innertubeClientVersion,
				"hl":            "en",
			},
		},
		"params": params,
	})
	if err != nil {
		return nil, err
	}

	endpoint := innertubeBase + "?key=" + innertubeKey
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", innertubeUA)

	audit.Logf("youtube: innertube get_transcript %s", videoID)
	resp, err := core.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("innertube get_transcript: HTTP %d: %s", resp.StatusCode, core.HTTPErrorBody(resp))
	}

	var data any
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("innertube decode: %w", err)
	}
	segs := walkSegments(data)
	if len(segs) == 0 {
		return nil, fmt.Errorf("no transcript segments returned (transcript may be disabled, or this is a live/members-only video)")
	}
	return segs, nil
}

// transcriptParamsRE pulls the `params` value out of the watch page's
// embedded JSON. The engagement panel for the transcript looks like
// "getTranscriptEndpoint":{"params":"…base64…"} and the value is a
// signed continuation token YouTube validates against the video.
var transcriptParamsRE = regexp.MustCompile(`"getTranscriptEndpoint":\s*\{\s*"params":\s*"([^"]+)"`)

// scrapeTranscriptParams fetches the watch page and extracts the
// transcript continuation token. Returns an error if the panel isn't
// present (videos without transcripts have no engagement panel).
func scrapeTranscriptParams(ctx context.Context, videoID string, audit *core.AuditLogger) (string, error) {
	watchURL := "https://www.youtube.com/watch?v=" + videoID
	audit.Logf("youtube: scraping watch page for transcript params")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, watchURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", innertubeUA)
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	resp, err := core.HTTPClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("watch page: HTTP %d: %s", resp.StatusCode, core.HTTPErrorBody(resp))
	}
	html := new(bytes.Buffer)
	if _, err := html.ReadFrom(resp.Body); err != nil {
		return "", err
	}
	m := transcriptParamsRE.FindSubmatch(html.Bytes())
	if len(m) < 2 {
		return "", fmt.Errorf("no transcript params in watch page (transcripts may be disabled for this video)")
	}
	return string(m[1]), nil
}

// encodeTranscriptParams is kept around for tests and as a last-resort
// fallback. It produces the base64-URL-encoded protobuf blob the
// get_transcript endpoint nominally expects:
// the get_transcript endpoint expects. The minimal accepted shape
// (per yt-dlp and Invidious as of 2026) is:
//
//	field 1 (length-delimited) = videoId      // tag 0x0a
//	field 2 (length-delimited) = ""           // tag 0x12, len 0
//	field 3 (varint)           = 1            // tag 0x18, value 1
//
// For an 11-char ID the wire bytes are 0x0a 0x0b <11 bytes> 0x12 0x00
// 0x18 0x01, base64 → "CgsAAA…GAE". A single-field encoding (just the
// videoId) was rejected with HTTP 400 — fields 2 and 3 are validated.
func encodeTranscriptParams(videoID string) string {
	var buf bytes.Buffer
	buf.WriteByte(0x0a)               // field 1, wire type 2
	buf.WriteByte(byte(len(videoID))) // length prefix
	buf.WriteString(videoID)
	buf.WriteByte(0x12) // field 2, wire type 2
	buf.WriteByte(0x00) // length 0
	buf.WriteByte(0x18) // field 3, wire type 0 (varint)
	buf.WriteByte(0x01) // value 1
	return base64.URLEncoding.EncodeToString(buf.Bytes())
}

// walkSegments hunts through the response JSON for any
// transcriptSegmentRenderer leaves. The shape has changed at least
// three times in recent years (initialSegments under
// transcriptSegmentListRenderer, then under
// transcriptSearchPanelRenderer.body, then nested differently again),
// so we tree-walk rather than commit to a single path.
func walkSegments(v any) []segment {
	var out []segment
	var visit func(any)
	visit = func(v any) {
		switch t := v.(type) {
		case map[string]any:
			if r, ok := t["transcriptSegmentRenderer"].(map[string]any); ok {
				if seg := extractSegment(r); seg.Text != "" {
					out = append(out, seg)
				}
				return // segments don't nest
			}
			for _, child := range t {
				visit(child)
			}
		case []any:
			for _, child := range t {
				visit(child)
			}
		}
	}
	visit(v)
	return out
}

func extractSegment(r map[string]any) segment {
	var seg segment
	if s, ok := r["startMs"].(string); ok {
		if n, err := strconv.Atoi(s); err == nil {
			seg.StartMs = n
		}
	}
	if s, ok := r["endMs"].(string); ok {
		if n, err := strconv.Atoi(s); err == nil {
			seg.DurationMs = n - seg.StartMs
		}
	}
	snippet, _ := r["snippet"].(map[string]any)
	seg.Text = extractSnippetText(snippet)
	seg.Offset = formatTimestamp(seg.StartMs)
	return seg
}

// extractSnippetText handles both shapes the snippet field can take —
// `simpleText` for one-piece captions, or a `runs` array when YouTube
// stitches together multiple segments (italicized words, hashtags,
// channel mentions, etc).
func extractSnippetText(m map[string]any) string {
	if m == nil {
		return ""
	}
	if s, ok := m["simpleText"].(string); ok && strings.TrimSpace(s) != "" {
		return s
	}
	runs, _ := m["runs"].([]any)
	var b strings.Builder
	for _, r := range runs {
		run, ok := r.(map[string]any)
		if !ok {
			continue
		}
		if t, ok := run["text"].(string); ok {
			b.WriteString(t)
		}
	}
	return b.String()
}

func formatTimestamp(ms int) string {
	secs := ms / 1000
	h, m, s := secs/3600, (secs/60)%60, secs%60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%d:%02d", m, s)
}
