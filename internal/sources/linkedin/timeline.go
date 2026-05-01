package linkedin

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/patrickdebois/social-skills/internal/bridge"
	"github.com/patrickdebois/social-skills/internal/core"
	"github.com/patrickdebois/social-skills/internal/htmlmd"
	"golang.org/x/net/html"
)

// TimelineKind is the slice of activity LinkedIn lets us request via
// the recent-activity URL path. Empty string maps to "all".
type TimelineKind string

const (
	TimelineAll       TimelineKind = "all"
	TimelinePosts     TimelineKind = "posts"
	TimelineComments  TimelineKind = "comments"
	TimelineReactions TimelineKind = "reactions"
)

// Activity is one item on a user's recent-activity feed.
type Activity struct {
	URN       string     // numeric activity id
	URL       string     // permalink: /feed/update/urn:li:activity:NNN/
	Body      string     // post text (markdown after htmlmd.Convert)
	Header    string     // e.g. "Patrick Debois reposted this" — empty for own posts
	Published *time.Time // best-effort, parsed from "5h •" / "2d •" relative stamps
	RelTime   string     // raw "5h •" / "2d •" / "Edited" string from LinkedIn
}

// FetchTimeline drives the bridge to /in/<user>/recent-activity/<kind>/
// and returns the visible activity cards. Bridge required (LinkedIn
// only renders the feed for a logged-in viewer).
//
// First-page only: LinkedIn lazy-loads further activities on scroll;
// adding scroll support is a follow-up. Typically returns 5–20 items.
func FetchTimeline(ctx context.Context, bridgeURL, user string, kind TimelineKind, max int, audit *core.AuditLogger) ([]Activity, string, error) {
	if user == "" {
		return nil, "", errors.New("linkedin timeline: empty user")
	}
	k := normaliseKind(kind)
	target := fmt.Sprintf("https://www.linkedin.com/in/%s/recent-activity/%s/", url.PathEscape(user), k)

	if bridgeURL == "" {
		bridgeURL = bridge.DefaultEndpoint
	}
	client := &bridge.Client{Endpoint: bridgeURL}
	htmlStr, _, _, err := client.GetHTML(ctx, target, audit)
	if err != nil {
		switch {
		case errors.Is(err, bridge.ErrBridgeUnreachable):
			return nil, target, fmt.Errorf("linkedin timeline: bridge daemon not running — `socialfetch bridge start`: %w", err)
		case errors.Is(err, bridge.ErrNoExtensionAttached):
			return nil, target, fmt.Errorf("linkedin timeline: no extension attached — open your browser with the PatAI extension running")
		default:
			return nil, target, fmt.Errorf("linkedin timeline: %w", err)
		}
	}

	doc, err := html.Parse(strings.NewReader(htmlStr))
	if err != nil {
		return nil, target, fmt.Errorf("linkedin timeline: parse: %w", err)
	}
	acts := extractActivities(doc, max)
	return acts, target, nil
}

func normaliseKind(k TimelineKind) string {
	switch strings.ToLower(string(k)) {
	case "", "all":
		return "all"
	case "posts", "shares":
		return "shares"
	case "comments":
		return "comments"
	case "reactions":
		return "reactions"
	default:
		return strings.ToLower(string(k))
	}
}

// extractActivities walks the parsed DOM, finds each
// `feed-shared-update-v2` card, and pulls the activity URN, body, and
// relative timestamp. Stops after `max` cards if max > 0.
func extractActivities(doc *html.Node, max int) []Activity {
	cards := findAll(doc, classContainsAny("feed-shared-update-v2"))
	out := make([]Activity, 0, len(cards))
	seen := map[string]bool{}
	for _, c := range cards {
		urn := activityURN(c)
		if urn == "" || seen[urn] {
			continue
		}
		seen[urn] = true

		a := Activity{URN: urn, URL: "https://www.linkedin.com/feed/update/urn:li:activity:" + urn + "/"}

		if body := findFirst(c, classContainsAny("update-components-text")); body != nil {
			a.Body = strings.TrimSpace(htmlmd.Convert(renderHTML(body)))
		}
		if hdr := findFirst(c, classContainsAny("update-components-header")); hdr != nil {
			a.Header = collapseSpaces(strings.TrimSpace(textOf(hdr)))
		}
		// Use classExact so we hit the time wrapper (class token
		// "update-components-actor__sub-description") instead of the
		// nearby `__sub-description-button-text` ("View my services").
		if sub := findFirst(c, classExact("update-components-actor__sub-description")); sub != nil {
			rel := collapseSpaces(strings.TrimSpace(textOf(sub)))
			rel = stripBoilerplate(rel)
			a.RelTime = rel
			if t := parseRelTime(rel, time.Now().UTC()); t != nil {
				a.Published = t
			}
		}

		out = append(out, a)
		if max > 0 && len(out) >= max {
			break
		}
	}
	return out
}

// activityURN reads the data-urn="urn:li:activity:NNN" attribute and
// returns the numeric id, or "" if the attribute is missing/malformed.
func activityURN(n *html.Node) string {
	v := getAttr(n, "data-urn")
	if v == "" {
		return ""
	}
	const prefix = "urn:li:activity:"
	if i := strings.Index(v, prefix); i >= 0 {
		return v[i+len(prefix):]
	}
	return ""
}

// stripBoilerplate removes the "View my services" / "Premium" cruft
// that LinkedIn smuggles into the same span as the relative timestamp.
func stripBoilerplate(s string) string {
	for _, drop := range []string{
		"View my services",
		"Premium",
		"Visible to anyone on or off LinkedIn",
		"Edited",
		"•",
	} {
		s = strings.ReplaceAll(s, drop, " ")
	}
	return collapseSpaces(strings.TrimSpace(s))
}

func collapseSpaces(s string) string {
	out := make([]byte, 0, len(s))
	prevSpace := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '\n' || c == '\t' || c == '\r' {
			c = ' '
		}
		if c == ' ' {
			if prevSpace {
				continue
			}
			prevSpace = true
		} else {
			prevSpace = false
		}
		out = append(out, c)
	}
	return string(out)
}

// relTimeRE matches "5h", "2d", "3w", "1mo", "1yr" — LinkedIn's
// truncated relative-time tokens. We use it to convert "5h •" into
// an approximate absolute time for sort/filter use.
var relTimeRE = regexp.MustCompile(`(\d+)\s*(s|m|h|d|w|mo|yr)\b`)

func parseRelTime(s string, now time.Time) *time.Time {
	m := relTimeRE.FindStringSubmatch(strings.ToLower(s))
	if m == nil {
		return nil
	}
	n := atoi(m[1])
	if n <= 0 {
		return nil
	}
	var d time.Duration
	switch m[2] {
	case "s":
		d = time.Duration(n) * time.Second
	case "m":
		d = time.Duration(n) * time.Minute
	case "h":
		d = time.Duration(n) * time.Hour
	case "d":
		d = time.Duration(n) * 24 * time.Hour
	case "w":
		d = time.Duration(n) * 7 * 24 * time.Hour
	case "mo":
		d = time.Duration(n) * 30 * 24 * time.Hour
	case "yr":
		d = time.Duration(n) * 365 * 24 * time.Hour
	default:
		return nil
	}
	t := now.Add(-d)
	return &t
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	return n
}
