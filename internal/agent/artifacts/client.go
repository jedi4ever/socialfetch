package artifacts

// client.go — operator-side counterpart to server.go. Wraps the
// HTTP wire shape with a small typed surface the social-agent
// CLI (and any future Go caller) can use.
//
// Cheap to construct, no resources held until a method is
// called. Substrate-agnostic: pass `BaseURL` from
// Session.WorkspaceURL — local docker resolves it to
// `http://127.0.0.1:<host-port>`, daytona will resolve it to a
// preview URL.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Client talks to a artifacts server.
type Client struct {
	// BaseURL is the http(s)://host:port the server listens on.
	// Required.
	BaseURL string

	// HTTP is the http.Client to use. nil = a default with a 60s
	// per-request timeout, generous for big-PNG pulls over the
	// daytona preview proxy.
	HTTP *http.Client

	// Token, when non-empty, is sent as Authorization: Bearer +
	// X-Daytona-Preview-Token on every request — same dual-header
	// shape internal/render/headless's DaemonClient uses.
	Token string
}

// httpClient returns the configured http.Client or a sensible
// default. Avoiding `c.HTTP = &http.Client{...}` in a constructor
// keeps Client safe for zero-value use.
func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 60 * time.Second}
}

// applyAuth sets the bearer/preview headers when Token is set.
// No-op for unauthed local-docker workspaces.
func (c *Client) applyAuth(req *http.Request) {
	if c.Token == "" {
		return
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("X-Daytona-Preview-Token", c.Token)
}

// List returns every file under /workspace, sorted by path.
// Empty workspace returns nil + nil (not an error).
func (c *Client) List(ctx context.Context) ([]FileEntry, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/artifacts/", nil)
	if err != nil {
		return nil, err
	}
	c.applyAuth(req)
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("list: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("list: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var entries []FileEntry
	if err := json.NewDecoder(resp.Body).Decode(&entries); err != nil {
		return nil, fmt.Errorf("list: decode: %w", err)
	}
	return entries, nil
}

// Get downloads one file by relative path. Returns its bytes.
// For large files prefer GetTo which streams to disk.
func (c *Client) Get(ctx context.Context, rel string) ([]byte, error) {
	req, err := c.getRequest(ctx, rel)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("get %s: HTTP %d: %s", rel, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return io.ReadAll(resp.Body)
}

// GetTo streams one file to disk at the given path. mkdir's the
// parent dir. Mode is 0o644 — the server's stored mode bits
// aren't preserved on the operator's host (operator may want
// different umask / ownership), kept simple.
func (c *Client) GetTo(ctx context.Context, rel, dst string) error {
	req, err := c.getRequest(ctx, rel)
	if err != nil {
		return err
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("get %s: HTTP %d: %s", rel, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := io.Copy(f, resp.Body); err != nil {
		return fmt.Errorf("write %s: %w", dst, err)
	}
	return nil
}

// PullAll downloads every file in /artifacts into destDir. Stats
// the result for the CLI's "pulled N files (X KB) → destDir"
// log line. Empty workspace is not an error: returns 0,0,nil.
func (c *Client) PullAll(ctx context.Context, destDir string) (count int, bytes int64, err error) {
	entries, err := c.List(ctx)
	if err != nil {
		return 0, 0, err
	}
	for _, e := range entries {
		dst := filepath.Join(destDir, filepath.FromSlash(e.Path))
		if err := c.GetTo(ctx, e.Path, dst); err != nil {
			return count, bytes, err
		}
		count++
		bytes += e.Size
	}
	return count, bytes, nil
}

// Delete removes one file from the container's workspace.
func (c *Client) Delete(ctx context.Context, rel string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.BaseURL+"/artifacts/"+rel, nil)
	if err != nil {
		return err
	}
	c.applyAuth(req)
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("delete %s: HTTP %d: %s", rel, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

// getRequest builds the GET request for a single-file fetch.
// Pulled out so Get and GetTo share the same auth + URL shape.
func (c *Client) getRequest(ctx context.Context, rel string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/artifacts/"+rel, nil)
	if err != nil {
		return nil, err
	}
	c.applyAuth(req)
	return req, nil
}
