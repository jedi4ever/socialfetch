package youtube

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/jedi4ever/socialfetch/internal/core"
)

// ytDlpAvailable reports whether `yt-dlp` is on $PATH. We check before
// trying it so the auto-provider can route around a missing binary
// silently.
func ytDlpAvailable() bool {
	_, err := exec.LookPath("yt-dlp")
	return err == nil
}

// fetchTranscriptYtDlp shells out to yt-dlp to download the
// auto-generated (or manual) subtitles in YouTube's `json3` format,
// then parses them into our segment shape.
//
// Why yt-dlp: it handles every YouTube anti-scraping cat-and-mouse
// move (PoToken, age-gates, region locks, signature ciphers, etc.)
// because the project is updated weekly. We trade a runtime dep for
// reliability.
func fetchTranscriptYtDlp(ctx context.Context, videoID, lang string, audit *core.AuditLogger) ([]segment, error) {
	bin, err := exec.LookPath("yt-dlp")
	if err != nil {
		return nil, fmt.Errorf("yt-dlp not on PATH (install with `brew install yt-dlp` or `pip install yt-dlp`)")
	}
	if lang == "" {
		lang = "en"
	}

	tmpDir, err := os.MkdirTemp("", "socialfetch-yt-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmpDir)

	audit.Logf("youtube: yt-dlp transcript %s", videoID)
	cmd := exec.CommandContext(ctx, bin,
		"--skip-download",
		"--write-subs",      // human-authored caption tracks (preferred)
		"--write-auto-subs", // fall back to YouTube's auto-generated ASR
		"--sub-langs", lang+",-live_chat",
		"--sub-format", "json3",
		"--quiet",
		"--no-warnings",
		"--output", filepath.Join(tmpDir, "%(id)s.%(ext)s"),
		"https://www.youtube.com/watch?v="+videoID,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		// yt-dlp prints "WARNING:" on stdout for transcript-disabled
		// videos but still exits 0; only escalate truly broken runs.
		return nil, fmt.Errorf("yt-dlp: %w: %s", err, strings.TrimSpace(string(out)))
	}

	matches, err := filepath.Glob(filepath.Join(tmpDir, "*.json3"))
	if err != nil || len(matches) == 0 {
		return nil, fmt.Errorf("yt-dlp produced no subtitle file (transcripts may be disabled, or the requested language %q is unavailable)", lang)
	}

	data, err := os.ReadFile(matches[0])
	if err != nil {
		return nil, err
	}
	return parseJSON3(data)
}

// parseJSON3 decodes YouTube's "json3" subtitle format. Each event has
// a start time, duration, and a list of utf8 segments — concatenating
// the segs gives one transcript line.
func parseJSON3(data []byte) ([]segment, error) {
	var doc struct {
		Events []struct {
			TStartMs    int `json:"tStartMs"`
			DDurationMs int `json:"dDurationMs"`
			Segs        []struct {
				Utf8 string `json:"utf8"`
			} `json:"segs"`
		} `json:"events"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse json3: %w", err)
	}
	var out []segment
	for _, e := range doc.Events {
		var text strings.Builder
		for _, s := range e.Segs {
			text.WriteString(s.Utf8)
		}
		t := strings.TrimSpace(text.String())
		// yt-dlp emits "newline-only" events to clear the previous
		// caption; skip those.
		if t == "" || t == "\n" {
			continue
		}
		out = append(out, segment{
			StartMs:    e.TStartMs,
			DurationMs: e.DDurationMs,
			Offset:     formatTimestamp(e.TStartMs),
			Text:       t,
		})
	}
	return out, nil
}
