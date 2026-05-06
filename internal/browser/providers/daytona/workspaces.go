package daytona

// Workspace operations — list, create, delete, preview-url. The
// types mirror the JSON shapes the Daytona API returns; field
// names follow the API's camelCase exactly so json tags can be
// short and one-to-one.

import (
	"context"
	"fmt"
	"net/url"
)

// Workspace is one Daytona sandbox row. We don't model every field
// the API returns — just what social-daytona reads. Add fields as
// the CLI grows. The API also includes an `env` map with all the
// secrets baked into the sandbox; we deliberately omit it from
// our Workspace struct so casual list operations don't pull
// production credentials into local state.
type Workspace struct {
	ID             string            `json:"id"`
	OrganizationID string            `json:"organizationId"`
	Name           string            `json:"name"`
	Snapshot       string            `json:"snapshot,omitempty"`
	Target         string            `json:"target,omitempty"` // region, e.g. "us"
	State          string            `json:"state,omitempty"`  // "started", "stopped", ...
	User           string            `json:"user,omitempty"`
	Class          string            `json:"class,omitempty"`
	CPU            int               `json:"cpu,omitempty"`
	Memory         int               `json:"memory,omitempty"` // GB
	Disk           int               `json:"disk,omitempty"`   // GB
	Public         bool              `json:"public,omitempty"`
	Labels         map[string]string `json:"labels,omitempty"`
	CreatedAt      string            `json:"createdAt,omitempty"`
}

// CreateWorkspaceRequest is the body for POST /api/workspace.
//
// Important: the field that drives "which snapshot/image to launch
// from" is `image` (not `snapshot`). The API silently falls back
// to its built-in default sandbox image when `image` is missing
// or unknown — there's no error, no warning, just a sandbox with
// the wrong contents. Painful to diagnose; we always send `image`.
//
// Only the knobs we actually use are modelled; the API has more
// (gpu, network allow-list, auto-archive, …) that we can add when
// they come up.
type CreateWorkspaceRequest struct {
	Image  string            `json:"image"`
	Name   string            `json:"name,omitempty"`
	Class  string            `json:"class,omitempty"` // small | medium | large
	Target string            `json:"target,omitempty"`
	CPU    int               `json:"cpu,omitempty"`
	Memory int               `json:"memory,omitempty"`
	Disk   int               `json:"disk,omitempty"`
	Env    map[string]string `json:"env,omitempty"`
	Labels map[string]string `json:"labels,omitempty"`
	Public bool              `json:"public,omitempty"`
	User   string            `json:"user,omitempty"`

	// AutoStopInterval is the inactivity window in minutes after
	// which Daytona stops the sandbox to save compute cost. 0 = no
	// auto-stop (runs until explicitly torn down). Default in the
	// Daytona API is 15 — too short for development sessions where
	// the operator wants the sandbox alive across pauses. Use 0
	// for "until I tell you to stop", or e.g. 240 for "auto-stop
	// after 4h idle".
	//
	// Pointer so we can distinguish "0 = explicit no auto-stop"
	// from "field omitted entirely (use API default)".
	AutoStopInterval *int `json:"autoStopInterval,omitempty"`

	// AutoArchiveInterval is the wall-clock window in minutes
	// after which Daytona archives a stopped sandbox (frees
	// resources but keeps state recoverable). 0 = use API default
	// (~7 days).
	AutoArchiveInterval *int `json:"autoArchiveInterval,omitempty"`
}

// listWorkspaceResponse is the GET /api/workspace shape. The list
// can come back as a bare array or wrapped in {items}; we handle
// both via a custom unmarshal.
type workspaceList []Workspace

// ListWorkspaces returns every sandbox visible to the API key.
// Caller filters by labels client-side — Daytona's list endpoint
// doesn't support label filters at request time today. Empty slice
// means no sandboxes (not an error).
func (c *Client) ListWorkspaces(ctx context.Context) ([]Workspace, error) {
	var got workspaceList
	if err := c.getJSON(ctx, "/workspace", nil, &got); err != nil {
		return nil, err
	}
	return got, nil
}

// CreateWorkspace POSTs to /api/workspace and returns the created
// row (with id assigned).
func (c *Client) CreateWorkspace(ctx context.Context, req CreateWorkspaceRequest) (*Workspace, error) {
	var out Workspace
	if err := c.postJSON(ctx, "/workspace", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteWorkspace removes one sandbox by id.
func (c *Client) DeleteWorkspace(ctx context.Context, id string) error {
	return c.deletePath(ctx, "/workspace/"+url.PathEscape(id), nil)
}

// PreviewURL is the response shape for the preview-url endpoint —
// signed tunnel URL the agent uses to reach a port inside a
// sandbox. The URL embeds the sandbox id, port, and an opaque
// short token (e.g. `5556-luclhu3ugafxputj.daytonaproxy01.net`),
// so callers do NOT add auth headers — including bearer headers
// here triggers Daytona's proxy to fall through to a browser-OAuth
// flow (307 → Auth0 → cached 404 at the CF edge for 60s, the flake
// we hit before this method was switched to signed URLs).
//
// Token is preserved in the response shape for backwards compat
// but populated as "" — auth is in the URL itself, downstream
// callers should not attach it as a bearer header.
type PreviewURL struct {
	URL   string `json:"url"`
	Token string `json:"token,omitempty"`
}

// GetPreviewURL fetches a SIGNED preview URL for a sandbox's port.
// Endpoint: GET /sandbox/<id>/ports/<port>/signed-preview-url?expires=<n>
// returns {sandboxId, port, url, token}. We return only `url` to
// callers (Token is "") — the URL embeds an opaque short-id that
// the proxy validates without any header.
//
// Why signed (not standard) preview URLs: the standard form
// (`/workspace/<id>/ports/<port>/preview-url`, full sandbox-id
// hostname + bearer token via `X-Daytona-Preview-Token`) is
// intermittently routed through Auth0's PKCE OAuth flow by the
// Daytona proxy, returning 307 → 404 → cached at CF edge for 60s.
// Signed URLs bypass that path entirely. See
// internal/browser/daemon.go's history for the empirical chase.
//
// expiresSec sets the signed URL's TTL in seconds. 0 = use the
// API default (3600s = 1h). Callers may want to refresh URLs
// proactively before the TTL expires (or rely on the daemon's
// 401-handling RefreshBackend path).
func (c *Client) GetPreviewURL(ctx context.Context, sandboxID string, port int, expiresSec int) (*PreviewURL, error) {
	if port <= 0 {
		return nil, fmt.Errorf("port must be > 0")
	}
	path := "/sandbox/" + url.PathEscape(sandboxID) +
		"/ports/" + itoa(port) +
		"/signed-preview-url"
	if expiresSec > 0 {
		path += "?expires=" + itoa(expiresSec)
	}
	var raw struct {
		URL   string `json:"url"`
		Token string `json:"token,omitempty"`
	}
	if err := c.getJSON(ctx, path, nil, &raw); err != nil {
		return nil, err
	}
	// Auth is embedded in the URL; intentionally drop the
	// API-returned token so downstream callers don't attach it as a
	// header (which would trigger the OAuth fallback).
	return &PreviewURL{URL: raw.URL}, nil
}
