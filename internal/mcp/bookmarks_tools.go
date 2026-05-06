package mcp

// MCP tools for the local-browser bookmarks reader. Mirror the
// CLI subcommand shape: list (filterable) + profiles (no args).
// Implementation lives in internal/platforms/bookmarks; this file
// only handles the MCP arg-parse + envelope.
//
// Why both an MCP tool and a CLI subcommand: the agent doing
// "what did I bookmark about X last week?" prefers a structured
// JSON response over parsing markdown, and the file-system access
// the bookmarks reader needs is local-machine anyway (Chrome's
// Bookmarks JSON sits in the user's home dir). Keeping the same
// surface as the CLI means an operator's mental model is one
// command-set across two transports.

import (
	"context"
	"os"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/jedi4ever/social-skills/internal/platforms/bookmarks"
)

// bookmarksListArgs mirrors the CLI flags. Empty strings = no
// filter; the bookmarks package's FilterOpts treats zero-values
// as "match everything." Limit defaults to 100 in the handler so
// agents can leave it off and still get a usable result.
type bookmarksListArgs struct {
	Profile        string  `json:"profile,omitempty"`
	AllProfiles    bool    `json:"all_profiles,omitempty"`
	Since          string  `json:"since,omitempty"`
	Until          string  `json:"until,omitempty"`
	URLContains    string  `json:"url_contains,omitempty"`
	TitleContains  string  `json:"title_contains,omitempty"`
	FolderContains string  `json:"folder_contains,omitempty"`
	Folder         string  `json:"folder,omitempty"`
	Limit          flexInt `json:"limit,omitempty"`
}

func addBookmarksListTool(s *server.MCPServer, cfg Config) {
	tool := mcp.NewTool("social_fetch_bookmarks_list",
		mcp.WithDescription("List local browser bookmarks (Chrome today; future: Twitter / Reddit server-side bookmarks). Reads Chrome's local Bookmarks JSON file directly — no extension, no daemon, just whatever Chrome has flushed to disk. Sorted by date_added desc (newest first). Use to answer 'what did I bookmark about X?' / 'show me everything in the AI folder'. Returns one row per bookmark with title, url, folder path, profile, date_added (RFC3339)."),
		mcp.WithString("profile", mcp.Description("Single profile to read (e.g. \"Default\", \"Profile 1\"). Empty = first available profile.")),
		mcp.WithBoolean("all_profiles", mcp.Description("Read every profile under the Chrome user-data dir. Profile field is ignored when this is true.")),
		mcp.WithString("since", mcp.Description("Only bookmarks added on/after this date (RFC3339 or YYYY-MM-DD).")),
		mcp.WithString("until", mcp.Description("Only bookmarks added before this date (RFC3339 or YYYY-MM-DD).")),
		mcp.WithString("url_contains", mcp.Description("Case-insensitive substring match on URL.")),
		mcp.WithString("title_contains", mcp.Description("Case-insensitive substring match on title.")),
		mcp.WithString("folder_contains", mcp.Description("Case-insensitive substring match on folder path (e.g. 'AI' matches 'bookmark_bar/Reading list/AI').")),
		mcp.WithString("folder", mcp.Description("Exact folder path with subtree match. Returns bookmarks whose folder equals this OR is nested under it (e.g. 'Bookmarks bar/AI' returns AI/, AI/papers/, AI/agents/). Case-insensitive. Falls back to $SOCIAL_FETCH_BOOKMARKS_ROOT_FOLDER when unset (older $SOCIAL_FETCH_BOOKMARKS_FOLDER works as an alias).")),
		mcp.WithNumber("limit", mcp.Description("Cap output at N rows (default 100; 0 = no cap).")),
	)
	s.AddTool(tool, mcp.NewTypedToolHandler(func(_ context.Context, _ mcp.CallToolRequest, args bookmarksListArgs) (*mcp.CallToolResult, error) {
		audit, closeAudit := openToolAudit(cfg, "bookmarks_list")
		defer closeAudit()

		since, err := parseBookmarksDate(args.Since)
		if err != nil {
			return mcp.NewToolResultError("since: " + err.Error()), nil
		}
		until, err := parseBookmarksDate(args.Until)
		if err != nil {
			return mcp.NewToolResultError("until: " + err.Error()), nil
		}

		// Env-var defaults — operator can scope every tool call to
		// one folder/profile by setting the env once. Explicit args
		// always win. Two env names accepted for the folder scope:
		// the new SOCIAL_FETCH_BOOKMARKS_ROOT_FOLDER reads better
		// ("this is the root of the scope"), the older
		// SOCIAL_FETCH_BOOKMARKS_FOLDER stays as an alias.
		profile := args.Profile
		if profile == "" {
			profile = strings.TrimSpace(os.Getenv("SOCIAL_FETCH_BOOKMARKS_PROFILE"))
		}
		folder := args.Folder
		if folder == "" {
			folder = strings.TrimSpace(os.Getenv("SOCIAL_FETCH_BOOKMARKS_ROOT_FOLDER"))
		}
		if folder == "" {
			folder = strings.TrimSpace(os.Getenv("SOCIAL_FETCH_BOOKMARKS_FOLDER"))
		}
		l := &bookmarks.Lister{
			Profile:     profile,
			AllProfiles: args.AllProfiles,
		}
		got, err := l.List(bookmarks.FilterOpts{
			Since:          since,
			Until:          until,
			URLContains:    args.URLContains,
			TitleContains:  args.TitleContains,
			FolderContains: args.FolderContains,
			Folder:         folder,
		})
		if err != nil {
			audit.Logf("bookmarks_list FAILED: %v", err)
			return mcp.NewToolResultError(err.Error()), nil
		}
		limit := int(args.Limit)
		if limit == 0 {
			limit = 100
		}
		if limit > 0 && len(got) > limit {
			got = got[:limit]
		}
		audit.Logf("bookmarks_list returned %d entries", len(got))
		return jsonResult(map[string]any{
			"count":     len(got),
			"bookmarks": got,
		})
	}))
}

func addBookmarksProfilesTool(s *server.MCPServer, cfg Config) {
	tool := mcp.NewTool("social_fetch_bookmarks_profiles",
		mcp.WithDescription("List Chrome profiles that have a local Bookmarks file present. Useful before calling social_fetch_bookmarks_list with a specific `profile` argument — the agent learns which names exist on this machine (\"Default\" vs \"Profile 1\" vs \"Profile 3\")."),
	)
	s.AddTool(tool, func(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		audit, closeAudit := openToolAudit(cfg, "bookmarks_profiles")
		defer closeAudit()

		l := &bookmarks.Lister{}
		profiles, err := l.Profiles()
		if err != nil {
			audit.Logf("bookmarks_profiles FAILED: %v", err)
			return mcp.NewToolResultError(err.Error()), nil
		}
		audit.Logf("bookmarks_profiles returned %d profiles", len(profiles))
		return jsonResult(map[string]any{
			"count":    len(profiles),
			"profiles": profiles,
		})
	})
}

// parseBookmarksDate accepts RFC3339 or YYYY-MM-DD. Mirrors the
// CLI's bookmarksParseDate; duplicated here so the MCP package
// doesn't import cmd/.
func parseBookmarksDate(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05Z", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, &errInvalidDate{s}
}

type errInvalidDate struct{ s string }

func (e *errInvalidDate) Error() string {
	return "invalid date " + e.s + " (use RFC3339 or YYYY-MM-DD)"
}
