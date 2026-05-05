package daytona

// Snapshot operations — list + look up. Push (multipart upload of
// a Docker image) is left to the daytona CLI subprocess in
// cmd/social-daytona; reimplementing it would mean dragging in the
// Docker image-spec dance which the official CLI already handles.

import (
	"context"
	"net/url"
)

// Snapshot is one row from /api/snapshots. Same field-naming
// convention as Workspace — model only what the CLI uses, add
// fields as needs surface.
type Snapshot struct {
	ID             string `json:"id"`
	OrganizationID string `json:"organizationId"`
	Name           string `json:"name"`
	ImageName      string `json:"imageName,omitempty"`
	State          string `json:"state,omitempty"` // "active", "building", ...
	Size           any    `json:"size,omitempty"`  // GB; arrives as float
	CPU            int    `json:"cpu,omitempty"`
	Memory         int    `json:"mem,omitempty"`
	Disk           int    `json:"disk,omitempty"`
	General        bool   `json:"general,omitempty"`
	CreatedAt      string `json:"createdAt,omitempty"`
}

// snapshotList is what /api/snapshots returns. The endpoint
// supports paging (`page`, `limit`); the default page is enough
// for our usage scale. If a project has >100 snapshots and we need
// pagination, surface it as a separate ListAll helper.
type snapshotList struct {
	Items []Snapshot `json:"items"`
}

// ListSnapshots returns the first page of snapshots visible to
// the org. Up to 100 by default — adequate for finding "the
// social-skills:0.13.x ones we just pushed." Caller filters by
// name client-side.
func (c *Client) ListSnapshots(ctx context.Context) ([]Snapshot, error) {
	var got snapshotList
	if err := c.getJSON(ctx, "/snapshots", nil, &got); err != nil {
		return nil, err
	}
	return got.Items, nil
}

// FindSnapshotByName returns the first matching snapshot or nil
// when none. Tag form ("name:version") matched verbatim against
// .Name. Used by social-daytona up to resolve "use the latest
// social-skills snapshot we pushed" without pinning a specific id.
func (c *Client) FindSnapshotByName(ctx context.Context, name string) (*Snapshot, error) {
	all, err := c.ListSnapshots(ctx)
	if err != nil {
		return nil, err
	}
	for i := range all {
		if all[i].Name == name {
			return &all[i], nil
		}
	}
	return nil, nil
}

// GetSnapshot loads one row by id. Used after CreateWorkspace
// returns a snapshot reference and we want to show the operator
// what they're spinning up against.
func (c *Client) GetSnapshot(ctx context.Context, id string) (*Snapshot, error) {
	var out Snapshot
	if err := c.getJSON(ctx, "/snapshots/"+url.PathEscape(id), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
