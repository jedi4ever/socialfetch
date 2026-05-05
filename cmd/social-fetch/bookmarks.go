package main

// CLI for `social-fetch bookmarks` — list bookmarks from local
// browser stores. Today's implementation supports Chrome only
// (--platform chrome, the default); other platforms (Twitter
// bookmarks, Reddit saved posts, etc.) plug in as additional
// case branches in dispatchBookmarks.
//
// Each fetched bookmark carries: Title, URL, DateAdded, Folder,
// Profile. The CLI emits markdown by default + JSON via -f json
// for tooling integrations.

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/jedi4ever/social-skills/internal/platforms/bookmarks"
)

// runBookmarks dispatches the bookmarks subcommands.
func runBookmarks(args []string) error {
	sub := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub = args[0]
		args = args[1:]
	}
	switch sub {
	case "", "list":
		return runBookmarksList(args)
	case "profiles":
		return runBookmarksProfiles(args)
	}
	if sub == "-h" || sub == "--help" {
		printBookmarksHelp()
		return nil
	}
	return fmt.Errorf("bookmarks: unknown subcommand %q (try `bookmarks --help`)", sub)
}

func printBookmarksHelp() {
	fmt.Print(`social-fetch bookmarks — list bookmarks from local browser stores

Usage:
  social-fetch bookmarks list [flags]          list matching bookmarks (default)
  social-fetch bookmarks profiles              list available profiles

Flags:
  --platform NAME       browser/platform to read from (default: chrome)
                        supported: chrome
                        future: twitter, reddit (server-side bookmarks)
  --profile NAME        single profile to read (e.g. "Default", "Profile 1");
                        default: first available profile
  --all-profiles        read every available profile, label results
  --chrome-root PATH    override Chrome user-data dir (auto-detected per OS)
  --since DATE          only bookmarks added on/after DATE (RFC3339 or 'YYYY-MM-DD')
  --until DATE          only bookmarks added before DATE
  --url-contains S      filter on URL substring (case-insensitive)
  --title-contains S    filter on title substring (case-insensitive)
  --folder-contains S   filter on folder path substring (case-insensitive)
  --folder PATH         scope to one folder + every nested subfolder, e.g.
                        --folder "Bookmarks bar/AI" returns AI/, AI/papers/,
                        AI/agents/, etc. Case-insensitive match. Combine
                        with --folder-contains for further narrowing.
  -n, --limit N         cap output at N rows (0 = no cap)
  -f, --format FMT      markdown (default) | json
  -h, --help            show this help

Environment:
  SOCIAL_FETCH_BOOKMARKS_ROOT_FOLDER  default value for --folder. Set once
                                       to scope every bookmarks call (CLI +
                                       MCP) to one folder + its subtree.
                                       Explicit --folder overrides.
                                       (Older name SOCIAL_FETCH_BOOKMARKS_FOLDER
                                       still works as an alias.)
  SOCIAL_FETCH_BOOKMARKS_PROFILE      default value for --profile.

Examples:
  social-fetch bookmarks list                          # newest 100 from default profile
  social-fetch bookmarks list --since 2026-04-01       # added in April 2026 onward
  social-fetch bookmarks list --folder-contains AI     # any folder path containing "AI"
  social-fetch bookmarks list --folder "Bookmarks bar/Reading list/AI"
                                                       # exact AI subtree
  social-fetch bookmarks list --all-profiles -f json   # every profile, JSON for piping
  social-fetch bookmarks profiles                      # which profiles are available
`)
}

// bookmarksFlags is the shared flag-parsing for list / profiles.
type bookmarksFlags struct {
	platform       string
	profile        string
	allProfiles    bool
	chromeRoot     string
	since          string
	until          string
	urlContains    string
	titleContains  string
	folderContains string
	folder         string // exact path + subtree (FilterOpts.Folder)
	limit          int
	format         string
}

func parseBookmarksFlags(args []string) (bookmarksFlags, error) {
	f := bookmarksFlags{platform: "chrome", format: "markdown", limit: 100}
	// Pre-populate from env so explicit flags can still override
	// later in the parse. Same pattern as other env-driven defaults
	// in this binary (SOCIAL_LEDGER_DIR, etc.).
	//
	// SOCIAL_FETCH_BOOKMARKS_ROOT_FOLDER is the "scope to this
	// folder + every subfolder under it" hint — set once and every
	// `bookmarks list` call (CLI + MCP) starts from there. The
	// older SOCIAL_FETCH_BOOKMARKS_FOLDER name still works as an
	// alias since it shipped briefly in v0.13.3-pre builds; remove
	// when no one's using it.
	if v := strings.TrimSpace(os.Getenv("SOCIAL_FETCH_BOOKMARKS_ROOT_FOLDER")); v != "" {
		f.folder = v
	} else if v := strings.TrimSpace(os.Getenv("SOCIAL_FETCH_BOOKMARKS_FOLDER")); v != "" {
		f.folder = v
	}
	if v := strings.TrimSpace(os.Getenv("SOCIAL_FETCH_BOOKMARKS_PROFILE")); v != "" {
		f.profile = v
	}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-h", "--help":
			printBookmarksHelp()
			os.Exit(0)
		case "--platform":
			i++
			if i >= len(args) {
				return f, fmt.Errorf("--platform needs a value")
			}
			f.platform = strings.ToLower(args[i])
		case "--profile":
			i++
			if i >= len(args) {
				return f, fmt.Errorf("--profile needs a value")
			}
			f.profile = args[i]
		case "--all-profiles":
			f.allProfiles = true
		case "--chrome-root":
			i++
			if i >= len(args) {
				return f, fmt.Errorf("--chrome-root needs a value")
			}
			f.chromeRoot = args[i]
		case "--since":
			i++
			if i >= len(args) {
				return f, fmt.Errorf("--since needs a value")
			}
			f.since = args[i]
		case "--until":
			i++
			if i >= len(args) {
				return f, fmt.Errorf("--until needs a value")
			}
			f.until = args[i]
		case "--url-contains":
			i++
			if i >= len(args) {
				return f, fmt.Errorf("--url-contains needs a value")
			}
			f.urlContains = args[i]
		case "--title-contains":
			i++
			if i >= len(args) {
				return f, fmt.Errorf("--title-contains needs a value")
			}
			f.titleContains = args[i]
		case "--folder-contains":
			i++
			if i >= len(args) {
				return f, fmt.Errorf("--folder-contains needs a value")
			}
			f.folderContains = args[i]
		case "--folder":
			i++
			if i >= len(args) {
				return f, fmt.Errorf("--folder needs a value")
			}
			f.folder = args[i]
		case "-n", "--limit":
			i++
			if i >= len(args) {
				return f, fmt.Errorf("--limit needs a value")
			}
			n, err := atoi(args[i])
			if err != nil || n < 0 {
				return f, fmt.Errorf("--limit: invalid value %q", args[i])
			}
			f.limit = n
		case "-f", "--format":
			i++
			if i >= len(args) {
				return f, fmt.Errorf("--format needs a value")
			}
			fmtVal := strings.ToLower(args[i])
			if fmtVal != "markdown" && fmtVal != "md" && fmtVal != "json" {
				return f, fmt.Errorf("--format: must be markdown or json")
			}
			f.format = fmtVal
		default:
			return f, fmt.Errorf("bookmarks: unknown argument %q", args[i])
		}
	}
	return f, nil
}

func runBookmarksList(args []string) error {
	flags, err := parseBookmarksFlags(args)
	if err != nil {
		return err
	}
	if flags.platform != "chrome" {
		return fmt.Errorf("bookmarks: platform %q not supported (today: chrome only)", flags.platform)
	}

	since, err := bookmarksParseDate(flags.since)
	if err != nil {
		return fmt.Errorf("--since: %w", err)
	}
	until, err := bookmarksParseDate(flags.until)
	if err != nil {
		return fmt.Errorf("--until: %w", err)
	}

	l := &bookmarks.Lister{
		ChromeRoot:  flags.chromeRoot,
		Profile:     flags.profile,
		AllProfiles: flags.allProfiles,
	}
	all, err := l.List(bookmarks.FilterOpts{
		Since:          since,
		Until:          until,
		URLContains:    flags.urlContains,
		TitleContains:  flags.titleContains,
		FolderContains: flags.folderContains,
		Folder:         flags.folder,
	})
	if err != nil {
		return err
	}
	if flags.limit > 0 && len(all) > flags.limit {
		all = all[:flags.limit]
	}

	switch flags.format {
	case "json":
		return json.NewEncoder(os.Stdout).Encode(all)
	default:
		return renderBookmarksMarkdown(all)
	}
}

func runBookmarksProfiles(args []string) error {
	flags, err := parseBookmarksFlags(args)
	if err != nil {
		return err
	}
	if flags.platform != "chrome" {
		return fmt.Errorf("bookmarks: platform %q not supported (today: chrome only)", flags.platform)
	}
	l := &bookmarks.Lister{ChromeRoot: flags.chromeRoot}
	profiles, err := l.Profiles()
	if err != nil {
		return err
	}
	if flags.format == "json" {
		return json.NewEncoder(os.Stdout).Encode(profiles)
	}
	if len(profiles) == 0 {
		fmt.Println("(no Chrome profiles with bookmarks found)")
		return nil
	}
	for _, p := range profiles {
		fmt.Println(p)
	}
	return nil
}

// bookmarksParseDate accepts RFC3339 or YYYY-MM-DD. Empty input
// returns zero time so callers don't have to nil-check. The
// existing parseDateFlag in main.go behaves identically — but
// renamed here to avoid the same-package collision; callers need
// the bookmark-specific shape only.
func bookmarksParseDate(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05Z", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid date %q (use RFC3339 or YYYY-MM-DD)", s)
}

// renderBookmarksMarkdown emits a one-row-per-bookmark markdown
// list. Format is intentionally simple: title as link, then a
// dim metadata line (date / folder / profile). Easy for the agent
// to read line-by-line; easy for a human to scan.
func renderBookmarksMarkdown(bs []bookmarks.Bookmark) error {
	if len(bs) == 0 {
		fmt.Println("(no bookmarks matched)")
		return nil
	}
	for _, b := range bs {
		title := b.Title
		if title == "" {
			title = b.URL
		}
		fmt.Printf("- [%s](%s)\n", title, b.URL)
		bits := []string{}
		if !b.DateAdded.IsZero() {
			bits = append(bits, b.DateAdded.Format("2006-01-02"))
		}
		if b.Folder != "" {
			bits = append(bits, "📁 "+b.Folder)
		}
		if b.Profile != "" {
			bits = append(bits, "👤 "+b.Profile)
		}
		if len(bits) > 0 {
			fmt.Printf("  %s\n", strings.Join(bits, " · "))
		}
	}
	return nil
}
