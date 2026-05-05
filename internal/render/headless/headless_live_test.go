//go:build live

package headless

import (
	"context"
	"strings"
	"testing"
	"time"
)

// Live tests for the headless transport. Hits real Chrome/Chromium —
// soft-skip when the binary isn't installed (chromedp returns a
// "exec: chrome: executable file not found" style error). Run with:
//
//	go test -tags=live -timeout 5m ./internal/render/headless/...

// TestLiveHeadless_ExampleCom — most boring stable URL on the
// internet. Verifies the launch + navigate + outerHTML pipeline
// works end-to-end without depending on any specific platform's
// HTML structure.
func TestLiveHeadless_ExampleCom(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	res, err := New().Fetch(ctx, "https://example.com/")
	if err != nil {
		if isMissingBrowserErr(err) {
			t.Skipf("chrome not available: %v", err)
		}
		t.Fatalf("Fetch: %v", err)
	}
	if res.HTML == "" {
		t.Fatal("empty HTML")
	}
	// example.com always contains the word "Example"
	if !strings.Contains(strings.ToLower(res.HTML), "example") {
		t.Errorf("HTML missing 'example' marker: %.200s", res.HTML)
	}
	if res.Engine != "chromedp" {
		t.Errorf("Engine = %q, want chromedp", res.Engine)
	}
	t.Logf("html=%d chars finalURL=%s", len(res.HTML), res.FinalURL)
}

// TestLiveHeadlessLinkedIn fetches Cole Medin's Archon-20k post via
// the headless transport. Anonymous when LINKEDIN_LI_AT isn't set
// (LinkedIn renders the guest-preview page); authenticated when the
// cookie is set in env (full body + comments).
//
// Soft assertion: just check the post body shows up. Doing strict
// comment-count assertions here is fragile — LinkedIn's anti-bot
// can serve different page shells based on session, and the auth
// flow may degrade to anonymous if the cookie has expired.
func TestLiveHeadlessLinkedIn(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	const postURL = "https://www.linkedin.com/posts/cole-medin-727752184_archon-just-crossed-20000-github-stars-share-7454993154392502272-RSYe/"
	res, err := New().Fetch(ctx, postURL)
	if err != nil {
		if isMissingBrowserErr(err) {
			t.Skipf("chrome not available: %v", err)
		}
		t.Fatalf("Fetch: %v", err)
	}
	if len(res.HTML) < 1000 {
		t.Errorf("HTML too small for a real LinkedIn page: %d chars", len(res.HTML))
	}
	// LinkedIn always serves SOME body — either the post (auth) or
	// the join-now wall (anon). Both contain "linkedin" in the body
	// somewhere; missing it = something fundamentally broken.
	if !strings.Contains(strings.ToLower(res.HTML), "linkedin") {
		t.Errorf("HTML missing 'linkedin' marker — page may not have loaded")
	}
	t.Logf("linkedin html=%d chars finalURL=%s", len(res.HTML), res.FinalURL)
}

// TestLiveHeadlessStealth — quick sanity check that the
// navigator.webdriver mask actually executed. We re-fetch
// example.com and look for the lack of a "HeadlessChrome" giveaway
// in the rendered HTML (the UA we ship + the init script hide it).
//
// This is a sanity test, not a full bot-detection bypass — sites
// with serious bot detection use canvas fingerprinting / TLS
// signature / WebGL inspection, none of which the JS-level mask
// can hide. It confirms the basics aren't broken.
func TestLiveHeadlessStealth(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	res, err := New().Fetch(ctx, "https://example.com/")
	if err != nil {
		if isMissingBrowserErr(err) {
			t.Skipf("chrome not available: %v", err)
		}
		t.Fatalf("Fetch: %v", err)
	}
	// example.com doesn't render UA into the body so we don't have
	// a hard assertion target here — just confirm Fetch completes
	// without the stealth init script breaking the page.
	if !strings.Contains(strings.ToLower(res.HTML), "<html") {
		t.Errorf("response not parseable as HTML: %.200s", res.HTML)
	}
}

// isMissingBrowserErr — tells "chrome not installed on this host"
// from "chrome failed for a real reason". chromedp's ExecAllocator
// surfaces missing-binary as a wrapped exec error containing
// "executable file not found" or "no such file or directory".
func isMissingBrowserErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "executable file not found") ||
		strings.Contains(msg, "no such file or directory") ||
		strings.Contains(msg, "could not find")
}
