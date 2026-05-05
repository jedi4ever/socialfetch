package daytona

// Workspace operations — list, create, delete, preview-url. The
// types mirror the JSON shapes the Daytona API returns; field
// names follow the API's camelCase exactly so json tags can be
// short and one-to-one.

import (
	"context"
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

// CreateWorkspaceRequest is the body for POST /api/workspace. Only
// the fields we actually pass; the API has more knobs (gpu, network
// allow-list, auto-archive, …) that we can add when we need them.
type CreateWorkspaceRequest struct {
	Snapshot string            `json:"snapshot"`
	Name     string            `json:"name,omitempty"`
	Class    string            `json:"class,omitempty"` // small | medium | large
	Target   string            `json:"target,omitempty"`
	CPU      int               `json:"cpu,omitempty"`
	Memory   int               `json:"memory,omitempty"`
	Disk     int               `json:"disk,omitempty"`
	Env      map[string]string `json:"env,omitempty"`
	Labels   map[string]string `json:"labels,omitempty"`
	Public   bool              `json:"public,omitempty"`
	User     string            `json:"user,omitempty"`
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
// sandbox. The signature embeds an expiration so URLs auto-expire
// (default 1h server-side; configurable via expires query param).
type PreviewURL struct {
	URL       string `json:"url"`
	ExpiresAt string `json:"expiresAt,omitempty"`
}

// GetPreviewURL fetches a signed URL for a sandbox's port. Returns
// the parsed shape; caller hands `.URL` to the agent / operator.
//
// Endpoint shape (unconfirmed against newer API versions; will
// adjust if 404). The Daytona CLI command equivalent is:
//
//	daytona preview-url <id> --port 5558 --expires 3600
func (c *Client) GetPreviewURL(ctx context.Context, sandboxID string, port int, expiresSec int) (*PreviewURL, error) {
	q := url.Values{}
	if port > 0 {
		q.Set("port", itoa(port))
	}
	if expiresSec > 0 {
		q.Set("expires", itoa(expiresSec))
	}
	var out PreviewURL
	if err := c.getJSON(ctx, "/workspace/"+url.PathEscape(sandboxID)+"/preview-url", q, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
