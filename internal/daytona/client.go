// Package daytona is a small REST client for the Daytona API.
//
// No official Go SDK exists today (the daytona CLI uses an internal
// apiclient that isn't published). The endpoints we need are few —
// snapshots / workspaces / preview-url — so a hand-rolled client
// keeps deps light and lets us evolve as Daytona's API changes.
//
// All calls go through one *http.Client with the Bearer token + org
// header already pinned, so individual op functions stay tight:
//
//	c, err := daytona.New()                       // env-driven
//	if err != nil { return err }
//	sandboxes, err := c.ListWorkspaces(ctx)
//
// Tested against https://app.daytona.io/api as of v0.171.0 of the
// Daytona CLI.
package daytona

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// itoa is a tiny strconv shim so call sites stay readable.
func itoa(n int) string { return strconv.Itoa(n) }

// Default endpoint; overridden by DAYTONA_API_URL.
const defaultAPIURL = "https://app.daytona.io/api"

// Client carries auth + connection settings. Cheap to construct;
// no resources held until a method is called.
type Client struct {
	BaseURL string
	APIKey  string
	OrgID   string
	HTTP    *http.Client
}

// New builds a Client from process env. Returns an error when the
// required vars are missing so the caller can show a clear message
// instead of getting a 401 on the first API call.
//
// Reads:
//
//	DAYTONA_API_KEY    Bearer token (required)
//	DAYTONA_ORG_ID     Org-scope header (required for most ops)
//	DAYTONA_API_URL    API base, defaults to https://app.daytona.io/api
//
// All three match the env names the official `daytona` CLI itself
// honours, so a single .env serves both tools.
func New() (*Client, error) {
	key := strings.TrimSpace(os.Getenv("DAYTONA_API_KEY"))
	if key == "" {
		return nil, fmt.Errorf("DAYTONA_API_KEY is not set (export it or source .env)")
	}
	org := strings.TrimSpace(os.Getenv("DAYTONA_ORG_ID"))
	if org == "" {
		return nil, fmt.Errorf("DAYTONA_ORG_ID is not set (find it under https://app.daytona.io/dashboard, then set in .env)")
	}
	base := strings.TrimSpace(os.Getenv("DAYTONA_API_URL"))
	if base == "" {
		base = defaultAPIURL
	}
	base = strings.TrimRight(base, "/")
	return &Client{
		BaseURL: base,
		APIKey:  key,
		OrgID:   org,
		HTTP:    &http.Client{Timeout: 60 * time.Second},
	}, nil
}

// do is the low-level request helper. Sets bearer auth + org header,
// reads body fully (so the timeout-bound context can complete before
// the caller json-unmarshals), surfaces non-2xx as a typed error
// containing the response body — Daytona returns plain JSON
// `{message,statusCode,error}` shapes that are useful to the
// operator.
func (c *Client) do(ctx context.Context, method, path string, body any, query url.Values) ([]byte, int, error) {
	full := c.BaseURL + path
	if len(query) > 0 {
		full += "?" + query.Encode()
	}
	var reqBody io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return nil, 0, fmt.Errorf("marshal body: %w", err)
		}
		reqBody = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, full, reqBody)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	req.Header.Set("X-Daytona-Organization-ID", c.OrgID)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Try to extract Daytona's standard error shape; fall back
		// to raw body when it's not JSON.
		var derr daytonaError
		if jerr := json.Unmarshal(out, &derr); jerr == nil && derr.Message != "" {
			return out, resp.StatusCode, &APIError{
				StatusCode: resp.StatusCode,
				Message:    derr.Message,
				Path:       derr.Path,
			}
		}
		return out, resp.StatusCode, &APIError{
			StatusCode: resp.StatusCode,
			Message:    strings.TrimSpace(string(out)),
		}
	}
	return out, resp.StatusCode, nil
}

// daytonaError mirrors the JSON Daytona returns on non-2xx —
// {path, timestamp, statusCode, error, message}. We surface
// Message + StatusCode + Path on APIError so callers can render
// or branch as needed.
type daytonaError struct {
	Path       string `json:"path"`
	StatusCode int    `json:"statusCode"`
	Error      string `json:"error"`
	Message    string `json:"message"`
}

// APIError is the typed error every public method on Client
// returns for non-2xx responses. Use errors.As to inspect.
type APIError struct {
	StatusCode int
	Message    string
	Path       string
}

func (e *APIError) Error() string {
	if e.Path != "" {
		return fmt.Sprintf("daytona: HTTP %d at %s: %s", e.StatusCode, e.Path, e.Message)
	}
	return fmt.Sprintf("daytona: HTTP %d: %s", e.StatusCode, e.Message)
}

// getJSON is a thin GET → unmarshal helper used by the read-side
// methods (List, Get). Body is ignored on GETs.
func (c *Client) getJSON(ctx context.Context, path string, query url.Values, out any) error {
	body, _, err := c.do(ctx, http.MethodGet, path, nil, query)
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(body, out)
}

// postJSON is the create-side helper. body is marshalled to JSON,
// response is unmarshalled into out (when non-nil).
func (c *Client) postJSON(ctx context.Context, path string, body, out any) error {
	respBody, _, err := c.do(ctx, http.MethodPost, path, body, nil)
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(respBody, out)
}

// deletePath issues a DELETE — used to tear down sandboxes.
// Returns nil on 2xx, typed *APIError on non-2xx.
func (c *Client) deletePath(ctx context.Context, path string, query url.Values) error {
	_, _, err := c.do(ctx, http.MethodDelete, path, nil, query)
	return err
}
