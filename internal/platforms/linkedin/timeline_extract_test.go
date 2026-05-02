package linkedin

import (
	"strings"
	"testing"
	"time"

	"golang.org/x/net/html"
)

const sampleActivityHTML = `
<html><body>
<main>
  <article class="feed-shared-update-v2" data-urn="urn:li:activity:7455960162130964480">
    <div class="update-components-actor__sub-description-button-text">View my services</div>
    <div class="update-components-actor__sub-description text-body-xsmall">
      <span>5h • Edited •</span>
    </div>
    <div class="update-components-text">
      <span>The state of the new "skills industry"</span>
    </div>
  </article>
  <article class="feed-shared-update-v2" data-urn="urn:li:activity:7455941026432757760">
    <div class="update-components-header">
      <span>Patrick Debois reposted this</span>
    </div>
    <div class="update-components-actor__sub-description">
      <span>2d •</span>
    </div>
    <div class="update-components-text">
      <span>Team. Org. Community.</span>
    </div>
  </article>
  <article class="feed-shared-update-v2" data-urn="urn:li:activity:7455941026432757760">
    <div class="update-components-text"><span>duplicate — should be deduped</span></div>
  </article>
</main>
</body></html>
`

func TestExtractActivities(t *testing.T) {
	doc, err := html.Parse(strings.NewReader(sampleActivityHTML))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	got := extractActivities(doc, 0)
	if len(got) != 2 {
		t.Fatalf("want 2 unique activities, got %d", len(got))
	}

	first := got[0]
	if first.URN != "7455960162130964480" {
		t.Errorf("first URN = %q", first.URN)
	}
	if !strings.Contains(first.URL, "urn:li:activity:7455960162130964480") {
		t.Errorf("first URL = %q", first.URL)
	}
	if !strings.Contains(first.Body, "skills industry") {
		t.Errorf("first body missing expected text: %q", first.Body)
	}
	if !strings.Contains(strings.ToLower(first.RelTime), "5h") {
		t.Errorf("first rel time should contain '5h': %q", first.RelTime)
	}
	if first.Published == nil {
		t.Error("first Published should be resolved from '5h'")
	}

	second := got[1]
	if !strings.Contains(strings.ToLower(second.Header), "reposted") {
		t.Errorf("second header should mention reposted: %q", second.Header)
	}
}

func TestExtractActivitiesRespectsMax(t *testing.T) {
	doc, err := html.Parse(strings.NewReader(sampleActivityHTML))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	got := extractActivities(doc, 1)
	if len(got) != 1 {
		t.Fatalf("max=1 should cap to 1, got %d", len(got))
	}
}

func TestParseRelTime(t *testing.T) {
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	cases := map[string]time.Duration{
		"5h •":                         5 * time.Hour,
		"2d •":                         2 * 24 * time.Hour,
		"3w":                           3 * 7 * 24 * time.Hour,
		"1mo •":                        30 * 24 * time.Hour,
		"View my services 5h • Edited": 5 * time.Hour,
		"Just now":                     0, // no match
	}
	for in, want := range cases {
		got := parseRelTime(in, now)
		if want == 0 {
			if got != nil {
				t.Errorf("%q: expected nil, got %v", in, got)
			}
			continue
		}
		if got == nil {
			t.Errorf("%q: expected match", in)
			continue
		}
		if !got.Equal(now.Add(-want)) {
			t.Errorf("%q: got %v, want %v", in, *got, now.Add(-want))
		}
	}
}

func TestNormaliseKind(t *testing.T) {
	cases := map[TimelineKind]string{
		"":          "all",
		"all":       "all",
		"posts":     "shares", // LinkedIn uses /shares/ for posts
		"shares":    "shares",
		"comments":  "comments",
		"reactions": "reactions",
	}
	for k, want := range cases {
		if got := normaliseKind(k); got != want {
			t.Errorf("normaliseKind(%q) = %q, want %q", k, got, want)
		}
	}
}
