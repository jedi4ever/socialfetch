package linkedin

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/patrickdebois/social-skills/internal/bridge"
	"github.com/patrickdebois/social-skills/internal/core"
	"github.com/patrickdebois/social-skills/internal/util/htmlmd"
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
// LinkedIn lazy-loads further activity as the user scrolls; we drive
// scroll-and-rescrape until we have `max` items, until two consecutive
// scrolls produce no new items, or until we hit a hard cap on
// iterations (avoids hangs on profiles with little activity).
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

	if err := client.Navigate(ctx, target, audit); err != nil {
		return nil, target, wrapBridgeErr(err)
	}

	acts, err := scrapeWithScroll(ctx, client, target, max, audit)
	if err != nil {
		return nil, target, wrapBridgeErr(err)
	}
	return acts, target, nil
}

// scrapeWithScroll alternates get_html / scroll until we have enough
// cards or no new ones appear. Hard caps prevent runaway: 12 scroll
// rounds is enough to surface ~50 cards on most profiles, and every
// round makes one bridge round-trip (~1-2s).
//
// Scroll distance and settle time are randomized per round to avoid a
// detectable fixed-cadence pattern — a real human scrolls in unequal
// pulses with unequal pauses. Distances are biased toward the middle
// of [1200, 3600]px and pauses to [700, 2400]ms.
func scrapeWithScroll(ctx context.Context, client *bridge.Client, target string, max int, audit *core.AuditLogger) ([]Activity, error) {
	const maxRounds = 12
	var acts []Activity
	prevCount := -1
	stalled := 0

	for round := 0; round < maxRounds; round++ {
		htmlStr, _, _, err := client.GetTabHTML(ctx, target, audit)
		if err != nil {
			return acts, err
		}
		doc, err := html.Parse(strings.NewReader(htmlStr))
		if err != nil {
			return acts, fmt.Errorf("linkedin timeline: parse: %w", err)
		}
		acts = extractActivities(doc, 0) // collect all visible, cap at the end
		if max > 0 && len(acts) >= max {
			break
		}
		if len(acts) == prevCount {
			stalled++
			if stalled >= 2 {
				// Two scrolls produced nothing new — assume end of feed.
				break
			}
		} else {
			stalled = 0
		}
		prevCount = len(acts)

		amount := randScrollAmount()
		if _, err := client.Scroll(ctx, amount, audit); err != nil {
			return acts, err
		}
		select {
		case <-time.After(randScrollPause()):
		case <-ctx.Done():
			return acts, ctx.Err()
		}
	}
	if max > 0 && len(acts) > max {
		acts = acts[:max]
	}
	return acts, nil
}

// randScrollAmount returns a plausibly-human scroll distance in pixels.
// Two pulses 60% of the time, single pulse 40% — humans rarely scroll
// the exact same distance twice in a row.
func randScrollAmount() int {
	base := 1200 + rand.IntN(2400) // [1200, 3600]
	if rand.IntN(10) < 6 {
		// Add a smaller secondary pulse to vary the centre of mass.
		base += 300 + rand.IntN(600)
	}
	return base
}

// randScrollPause returns a settle time after a scroll. Long pauses
// (>1.5s) happen ~30% of the time to mimic a reader skimming a card.
func randScrollPause() time.Duration {
	base := 700 + rand.IntN(900) // [700, 1600]
	if rand.IntN(10) < 3 {
		base += 600 + rand.IntN(800) // occasional long read
	}
	return time.Duration(base) * time.Millisecond
}

func wrapBridgeErr(err error) error {
	switch {
	case errors.Is(err, bridge.ErrBridgeUnreachable):
		return fmt.Errorf("linkedin timeline: bridge daemon not running — `socialfetch bridge start`: %w", err)
	case errors.Is(err, bridge.ErrNoExtensionAttached):
		return fmt.Errorf("linkedin timeline: no extension attached — open your browser with the PatAI extension running")
	default:
		return fmt.Errorf("linkedin timeline: %w", err)
	}
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

		// On /recent-activity/comments/ each card holds the original
		// post AND the user's comment. Prefer the comment content when
		// present so the timeline shows what the user *said*, not the
		// post they replied to. Same for inline-show-more variants.
		if body := findFirst(c, classContainsAny(
			"comments-comment-item__main-content",
			"comments-comment-item-content-body",
		)); body != nil {
			a.Body = strings.TrimSpace(htmlmd.Convert(renderHTML(body)))
		} else if body := findFirst(c, classContainsAny("update-components-text")); body != nil {
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
