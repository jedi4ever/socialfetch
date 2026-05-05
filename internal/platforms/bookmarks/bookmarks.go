// Package bookmarks reads Chrome's local Bookmarks JSON file and
// exposes the entries as []Bookmark for the `social-fetch
// bookmarks list` CLI + (future) MCP tool.
//
// Why this exists: Chrome stores all bookmarks in a single JSON
// tree per profile at a well-known path. Reading it directly is
// orders of magnitude simpler than scraping the bookmarks UI or
// using the Chrome extension API. Trade-off: we only see what
// Chrome has flushed to disk (bookmarks added moments before are
// usually written within a second or two — fine for batch
// research workflows).
//
// Profile shape on macOS:
//
//	~/Library/Application Support/Google/Chrome/<Profile>/Bookmarks
//
// where <Profile> is `Default`, `Profile 1`, `Profile 2`, … per
// Chrome user. We auto-detect available profiles by listing the
// parent dir; explicit `--profile` overrides.
package bookmarks

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Bookmark is one entry pulled from a Chrome bookmarks tree.
// Folder is the slash-joined path to the entry's parent
// (e.g. "Bookmarks bar/Reading list/AI"). DateAdded is the
// time the user bookmarked the URL — useful for date-range
// filtering ("show me what I bookmarked last week").
type Bookmark struct {
	Title     string    `json:"title"`
	URL       string    `json:"url"`
	DateAdded time.Time `json:"date_added"`
	LastUsed  time.Time `json:"date_last_used,omitempty"`
	Folder    string    `json:"folder,omitempty"`
	Profile   string    `json:"profile"`
	GUID      string    `json:"guid,omitempty"`
}

// Lister reads bookmarks from one or more Chrome profile
// directories. Cheap to construct; the actual file reads happen
// in List(). Profile defaults pick the available profile when
// unspecified — single-profile users don't need to know the name.
type Lister struct {
	// ChromeRoot overrides the auto-detected Chrome user-data
	// directory. Empty = auto-detect for the OS. Tests inject a
	// temp dir.
	ChromeRoot string

	// Profile selects a single profile by name (e.g. "Default",
	// "Profile 1"). Empty + AllProfiles=false = auto-pick the
	// first profile that has a Bookmarks file.
	Profile string

	// AllProfiles, when true, reads bookmarks from every profile
	// found under ChromeRoot. Profile field is ignored.
	AllProfiles bool
}

// FilterOpts narrows which bookmarks are returned. Zero-value
// means "no filter". DateAdded comparisons are inclusive on Since,
// exclusive on Until — same convention as the timeline subcommand.
type FilterOpts struct {
	Since          time.Time
	Until          time.Time
	URLContains    string // case-insensitive substring match on URL
	TitleContains  string // case-insensitive substring match on Title
	FolderContains string // case-insensitive substring match on Folder

	// Folder is an exact-path scope: returns bookmarks whose
	// Folder equals this value OR is rooted at it (i.e. starts
	// with `<Folder>/`). Use to scope to one folder including
	// every nested subfolder, e.g. Folder="Bookmarks bar/AI"
	// returns AI/, AI/papers/, AI/agents/, etc. — but NOT a
	// random "AI" folder elsewhere in the tree.
	//
	// Folder paths are case-sensitive on input (Chrome's stored
	// folder names are case-preserving) but matched
	// case-insensitively to keep the CLI ergonomic — operators
	// don't need to remember whether they capitalised "AI" or
	// "ai" when they created the folder.
	//
	// Combine with FolderContains for "everything in AI matching
	// 'arxiv' anywhere in the path" — both filters AND together.
	Folder string
}

// chromeUserDataRoot returns the platform-specific path to
// Chrome's user-data parent directory. Linux/Windows variants
// included so the same binary works everywhere; tests set
// ChromeRoot directly to bypass.
func chromeUserDataRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "Google", "Chrome"), nil
	case "linux":
		return filepath.Join(home, ".config", "google-chrome"), nil
	case "windows":
		appData := os.Getenv("LOCALAPPDATA")
		if appData == "" {
			return "", fmt.Errorf("LOCALAPPDATA not set")
		}
		return filepath.Join(appData, "Google", "Chrome", "User Data"), nil
	}
	return "", fmt.Errorf("unsupported OS %q", runtime.GOOS)
}

// Profiles returns the list of Chrome profile names that have a
// Bookmarks file present. Auto-detected from ChromeRoot's
// directory contents. Names match Chrome's conventions (`Default`,
// `Profile 1`, `Profile 2`, …); operators see them in
// chrome://settings/manageProfile.
//
// Empty result is not an error — just means Chrome hasn't been
// run yet on this user account, or the user-data dir is somewhere
// non-default.
func (l *Lister) Profiles() ([]string, error) {
	root := l.ChromeRoot
	if root == "" {
		r, err := chromeUserDataRoot()
		if err != nil {
			return nil, err
		}
		root = r
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("read chrome dir %s: %w", root, err)
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		// Heuristic: Default + Profile N + Guest Profile are the
		// real profiles. Skip the rest of Chrome's bookkeeping
		// directories (Avatars, Crashpad, etc.).
		if name != "Default" && !strings.HasPrefix(name, "Profile ") && name != "Guest Profile" {
			continue
		}
		bm := filepath.Join(root, name, "Bookmarks")
		if _, err := os.Stat(bm); err != nil {
			continue // profile dir without a Bookmarks file
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out, nil
}

// List returns all bookmarks matching the configured profile
// selection + filter. Sorted by DateAdded descending (newest
// first) so the typical "what did I bookmark recently?" query
// gets a useful prefix without needing a separate sort.
func (l *Lister) List(filter FilterOpts) ([]Bookmark, error) {
	root := l.ChromeRoot
	if root == "" {
		r, err := chromeUserDataRoot()
		if err != nil {
			return nil, err
		}
		root = r
	}

	var profiles []string
	if l.AllProfiles {
		ps, err := l.Profiles()
		if err != nil {
			return nil, err
		}
		profiles = ps
	} else if l.Profile != "" {
		profiles = []string{l.Profile}
	} else {
		// No explicit profile + not all → pick the first
		// available so single-profile users don't have to know
		// the name.
		ps, err := l.Profiles()
		if err != nil {
			return nil, err
		}
		if len(ps) == 0 {
			return nil, fmt.Errorf("no Chrome profiles with bookmarks found under %s", root)
		}
		profiles = []string{ps[0]}
	}

	var all []Bookmark
	for _, p := range profiles {
		bs, err := readProfileBookmarks(root, p)
		if err != nil {
			return nil, fmt.Errorf("profile %q: %w", p, err)
		}
		all = append(all, bs...)
	}

	out := all[:0]
	for _, b := range all {
		if !filter.matches(b) {
			continue
		}
		out = append(out, b)
	}

	sort.Slice(out, func(i, j int) bool {
		return out[i].DateAdded.After(out[j].DateAdded)
	})
	return out, nil
}

// matches applies the filter to a single bookmark. Returns true
// for a zero-value filter — operators get every bookmark when
// they don't pass any narrowing flags.
func (f FilterOpts) matches(b Bookmark) bool {
	if !f.Since.IsZero() && b.DateAdded.Before(f.Since) {
		return false
	}
	if !f.Until.IsZero() && !b.DateAdded.Before(f.Until) {
		return false
	}
	if f.URLContains != "" && !strings.Contains(strings.ToLower(b.URL), strings.ToLower(f.URLContains)) {
		return false
	}
	if f.TitleContains != "" && !strings.Contains(strings.ToLower(b.Title), strings.ToLower(f.TitleContains)) {
		return false
	}
	if f.FolderContains != "" && !strings.Contains(strings.ToLower(b.Folder), strings.ToLower(f.FolderContains)) {
		return false
	}
	if f.Folder != "" {
		want := strings.ToLower(strings.Trim(f.Folder, "/"))
		got := strings.ToLower(b.Folder)
		// Match exact folder OR any nested subfolder. Trailing
		// slash on `want` is stripped above so `--folder AI/` and
		// `--folder AI` both behave the same.
		if got != want && !strings.HasPrefix(got, want+"/") {
			return false
		}
	}
	return true
}

// readProfileBookmarks parses one profile's Bookmarks JSON and
// flattens the tree into []Bookmark. Folder paths are joined with
// "/" so a bookmark inside "Bookmarks bar > Reading list > AI"
// reports Folder="Bookmarks bar/Reading list/AI" — readable in
// markdown output and easy to filter on.
func readProfileBookmarks(root, profile string) ([]Bookmark, error) {
	path := filepath.Join(root, profile, "Bookmarks")
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc bookmarksDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	var out []Bookmark
	// roots typically contains "bookmark_bar" + "other" +
	// "synced". We label each top-level the way Chrome's UI
	// labels them so filters like --folder=bookmark_bar are
	// stable across profiles.
	for label, node := range doc.Roots {
		walkNode(node, label, profile, &out)
	}
	return out, nil
}

// walkNode walks one folder/url node recursively, appending Bookmark
// rows for every "url"-typed leaf. The folder path threads down via
// the path argument so leaves know the full ancestry.
func walkNode(n *node, path, profile string, out *[]Bookmark) {
	if n == nil {
		return
	}
	switch n.Type {
	case "url":
		*out = append(*out, Bookmark{
			Title:     n.Name,
			URL:       n.URL,
			DateAdded: parseChromeTime(n.DateAdded),
			LastUsed:  parseChromeTime(n.DateLastUsed),
			Folder:    path,
			Profile:   profile,
			GUID:      n.GUID,
		})
	case "folder":
		// Folder name appended to path; root-level folders
		// already supply their own label so we don't duplicate.
		next := path
		if n.Name != "" && path != "" {
			next = path + "/" + n.Name
		} else if n.Name != "" {
			next = n.Name
		}
		for _, c := range n.Children {
			walkNode(c, next, profile, out)
		}
	}
}

// bookmarksDoc / node mirror the slice of Chrome's JSON we read.
// Chrome's actual schema has more fields (sync metadata, favicons,
// etc.) — we ignore everything except the URL tree.
type bookmarksDoc struct {
	Roots map[string]*node `json:"roots"`
}

type node struct {
	Type         string  `json:"type"` // "url" or "folder"
	Name         string  `json:"name"`
	URL          string  `json:"url,omitempty"`
	GUID         string  `json:"guid,omitempty"`
	DateAdded    string  `json:"date_added,omitempty"`
	DateLastUsed string  `json:"date_last_used,omitempty"`
	Children     []*node `json:"children,omitempty"`
}

// parseChromeTime converts a Chrome bookmark timestamp string
// (microseconds since 1601-01-01 UTC, Windows FILETIME-derived)
// into a Go time.Time. Empty / unparseable → zero time, which
// the UI renders as a blank rather than 1601 nonsense.
//
// The 11_644_473_600_000_000 constant is the µs offset from
// 1601-01-01 to 1970-01-01 — the Unix epoch in Chrome's frame.
func parseChromeTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n <= 0 {
		return time.Time{}
	}
	const chromeEpochOffsetMicros = 11644473600000000
	unixMicros := n - chromeEpochOffsetMicros
	if unixMicros <= 0 {
		return time.Time{}
	}
	return time.UnixMicro(unixMicros).UTC()
}
