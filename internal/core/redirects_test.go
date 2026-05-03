package core

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestTrackRedirectsCycleDetection verifies that a server-side
// redirect loop (302 → same URL forever) gets caught after the
// tolerance threshold rather than running until the 10-hop ceiling.
//
// The server here mirrors milvus.io's real behavior — emit a Set-
// Cookie on every 302 to the same URL. Without a cookie jar +
// cycle detection this would loop until the 10-hop limit; with
// them, the cookie jar carries the cookie forward, the server
// keeps sending 302s anyway, and the cycle detector stops it
// after `maxSameURLHits` revisits.
func TestTrackRedirectsCycleDetection(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "challenge", Value: "x", MaxAge: 60})
		w.Header().Set("Location", r.URL.Path)
		w.WriteHeader(http.StatusFound)
	}))
	defer srv.Close()

	resp, err := HTTPClient.Get(srv.URL + "/loop")
	if err == nil {
		resp.Body.Close()
		t.Fatal("expected redirect-loop error, got nil")
	}
	if !errors.Is(err, ErrRedirectLoop) {
		t.Errorf("error should wrap ErrRedirectLoop, got %v", err)
	}
}

// TestTrackRedirectsTolerantOfBriefSameURL verifies that ONE
// same-URL hop is allowed (cookie-challenge round-trip needs it)
// even though we'd flag THREE-plus as a loop. We confirm by sending
// 302→same-URL once and then 200 — should succeed, not error.
func TestTrackRedirectsTolerantOfBriefSameURL(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if hits == 1 {
			http.SetCookie(w, &http.Cookie{Name: "session", Value: "ok", MaxAge: 60})
			w.Header().Set("Location", r.URL.Path)
			w.WriteHeader(http.StatusFound)
			return
		}
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	resp, err := HTTPClient.Get(srv.URL + "/cookie-challenge")
	if err != nil {
		t.Fatalf("expected success after cookie round-trip, got %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}

func TestNormalizeRedirectURL(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"https://Example.COM/foo", "https://example.com/foo"},
		{"https://example.com/foo#frag", "https://example.com/foo"},
		{"https://example.com/foo?q=1", "https://example.com/foo?q=1"}, // query stays
	}
	for _, c := range cases {
		if got := normalizeRedirectURL(c.in); got != c.want {
			t.Errorf("normalizeRedirectURL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// Sanity — the cookie jar is wired up so cookies set on one
// request are sent back on the next. Without this, redirect-
// loop fixes would still fail on real cookie-challenge sites.
func TestHTTPClientCarriesCookies(t *testing.T) {
	var sawCookie string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie("test")
		if err == nil {
			sawCookie = c.Value
		}
		http.SetCookie(w, &http.Cookie{Name: "test", Value: "abc123", MaxAge: 60})
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	// First request — no cookie.
	resp, err := HTTPClient.Get(srv.URL + "/")
	if err != nil {
		t.Fatalf("first GET: %v", err)
	}
	resp.Body.Close()
	if sawCookie != "" {
		t.Errorf("first GET should not have a cookie, saw %q", sawCookie)
	}
	// Second request — cookie should be carried.
	resp, err = HTTPClient.Get(srv.URL + "/")
	if err != nil {
		t.Fatalf("second GET: %v", err)
	}
	resp.Body.Close()
	if sawCookie != "abc123" {
		t.Errorf("second GET should carry cookie, saw %q", sawCookie)
	}
	// Sanity — make sure the URL string was actually used.
	if !strings.HasPrefix(srv.URL, "http://") {
		t.Errorf("test server URL unexpected: %s", srv.URL)
	}
}
