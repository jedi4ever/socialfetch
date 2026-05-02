package linkedin

import (
	"strings"
	"testing"
	"time"

	"golang.org/x/net/html"

	"github.com/patrickdebois/social-skills/internal/core"
)

// LinkedIn content-search results fragment captured from the live
// site. Same `feed-shared-update-v2` envelope as profile activity, so
// extractActivities works unchanged. The selector-drift watch is
// done in timeline_extract_test.go for the shared selectors; this
// test pins the specifically-search-related plumbing (URL building,
// Activity → SearchResult conversion).
const linkedInSearchPage = `<!DOCTYPE html>
<html><body>
  <ul class="reusable-search__entity-result-list">
    <li>
      <div class="feed-shared-update-v2" data-urn="urn:li:activity:7220000000000000001">
        <div class="update-components-actor">
          <div class="update-components-actor__sub-description">2d ago</div>
        </div>
        <div class="update-components-text">
          <span>Harness engineering changes how we ship coding agents — fewer model upgrades, more deterministic controls.</span>
        </div>
        <div class="update-components-header">Patrick Debois on harness engineering</div>
      </div>
    </li>
    <li>
      <div class="feed-shared-update-v2" data-urn="urn:li:activity:7220000000000000002">
        <div class="update-components-actor">
          <div class="update-components-actor__sub-description">5d ago</div>
        </div>
        <div class="update-components-text">
          <span>Just shipped a research mode for our internal tooling. Decompose → fan-out → synthesize.</span>
        </div>
      </div>
    </li>
  </ul>
</body></html>`

func TestSearchExtractorParsesCards(t *testing.T) {
	doc, err := html.Parse(strings.NewReader(linkedInSearchPage))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	acts := FindSearchActivities(doc, 0)
	if len(acts) != 2 {
		t.Fatalf("expected 2 activities, got %d", len(acts))
	}
	if acts[0].URN != "7220000000000000001" {
		t.Errorf("URN[0] = %q", acts[0].URN)
	}
	if !strings.Contains(acts[0].Body, "Harness engineering") {
		t.Errorf("body[0] = %q (expected to contain 'Harness engineering')", acts[0].Body)
	}
	if acts[1].RelTime != "5d ago" {
		t.Errorf("RelTime[1] = %q, want '5d ago'", acts[1].RelTime)
	}
}

func TestSearchExtractorMaxCap(t *testing.T) {
	doc, _ := html.Parse(strings.NewReader(linkedInSearchPage))
	acts := FindSearchActivities(doc, 1)
	if len(acts) != 1 {
		t.Errorf("max=1 returned %d items", len(acts))
	}
}

func TestToSearchResult(t *testing.T) {
	pub := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	a := Activity{
		URN:       "999",
		URL:       "https://www.linkedin.com/feed/update/urn:li:activity:999/",
		Body:      "First line is the headline\n\nSecond paragraph with detail.",
		Header:    "Patrick on something",
		Published: &pub,
	}
	r := toSearchResult(a)
	if r.Source != "linkedin" {
		t.Errorf("source = %q", r.Source)
	}
	if r.URL != a.URL {
		t.Errorf("url = %q", r.URL)
	}
	if r.Title != "First line is the headline" {
		t.Errorf("title = %q", r.Title)
	}
	if r.Published == nil || !r.Published.Equal(pub) {
		t.Errorf("published = %v", r.Published)
	}
}

func TestToSearchResultEmptyBodyFallsBackToHeader(t *testing.T) {
	a := Activity{
		URN:    "x",
		URL:    "https://www.linkedin.com/feed/update/urn:li:activity:x/",
		Body:   "",
		Header: "header text",
	}
	r := toSearchResult(a)
	if r.Title != "header text" {
		t.Errorf("title = %q, want 'header text'", r.Title)
	}
}

func TestBuildSearchURLAppliesDateWindow(t *testing.T) {
	cases := []struct {
		name string
		ago  time.Duration
		want string
	}{
		{"24h ago", 12 * time.Hour, `"past-24h"`},
		{"week ago", 5 * 24 * time.Hour, `"past-week"`},
		{"month ago", 20 * 24 * time.Hour, `"past-month"`},
		{"year ago — no preset", 365 * 24 * time.Hour, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			after := time.Now().Add(-tc.ago)
			u := buildSearchURL("test query", core.SearchOptions{After: &after})
			if tc.want == "" {
				if strings.Contains(u, "datePosted=") {
					t.Errorf("did not expect datePosted in %q", u)
				}
				return
			}
			// The URL is encoded, so the want value is too — embed it
			// the way url.Values.Encode would.
			wantEnc := strings.ReplaceAll(strings.ReplaceAll(tc.want, "\"", "%22"), "-", "-")
			if !strings.Contains(u, "datePosted="+wantEnc) {
				t.Errorf("expected datePosted=%s in %q", wantEnc, u)
			}
		})
	}
}
