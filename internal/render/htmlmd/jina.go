package htmlmd

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/jedi4ever/socialfetch/internal/core"
)

// jinaReaderBase is the public Jina Reader endpoint. The full URL
// shape is `https://r.jina.ai/<target-url>` — no key needed for the
// free tier, no query-string params, no body. Returns clean markdown
// of the target page.
//
// Why this is useful: Jina executes JavaScript, sidesteps Cloudflare
// challenges that block direct curl, and applies its own readability
// extraction — drop-in for sites where our local fetch + extract
// pipeline returns a 403 or junk content.
const jinaReaderBase = "https://r.jina.ai/"

// JinaReader hits r.jina.ai. Free tier has no key requirement; paid
// tiers exist but we don't authenticate today.
//
// Reuses core.HTTPClient.Transport for audit instrumentation so each
// call lands in the audit log next to the rest of the platform's HTTP
// activity.
type JinaReader struct {
	BaseURL string
	Client  *http.Client
}

// NewJinaReader builds a reader with a 60s timeout — Jina's worst
// case (heavily-JS-rendered pages it has to render headlessly) is
// 30-45s; the headroom keeps timeouts rare without letting one bad
// page hang an entire batch fetch.
func NewJinaReader() *JinaReader {
	return &JinaReader{
		BaseURL: jinaReaderBase,
		Client: &http.Client{
			Timeout:   60 * time.Second,
			Transport: core.HTTPClient.Transport,
		},
	}
}

func (j *JinaReader) Read(ctx context.Context, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, j.BaseURL+url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", core.UserAgent)
	// Jina supports an `Accept: text/markdown` hint; without it it
	// still returns markdown, but the explicit header makes the intent
	// readable in audit traces.
	req.Header.Set("Accept", "text/markdown")

	resp, err := j.Client.Do(req)
	if err != nil {
		return "", fmt.Errorf("jina reader: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("jina reader: HTTP %d: %s", resp.StatusCode, core.HTTPErrorBody(resp))
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("jina reader: read: %w", err)
	}
	return string(body), nil
}
