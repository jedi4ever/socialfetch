package headless

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestDaemonClient_Reachable covers the cheap probe that decides
// daemon-mode vs in-process. Pointed at a httptest server it
// returns true when /status answers; pointed at a closed port it
// returns false fast.
func TestDaemonClient_Reachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/status" {
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(statusResponse{PoolSize: 2})
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	c := &DaemonClient{BaseURL: srv.URL}
	if !c.Reachable(context.Background()) {
		t.Error("expected reachable=true")
	}

	// Closed port — reachable should return false within the
	// 250 ms cap, not hang.
	c2 := &DaemonClient{BaseURL: "http://127.0.0.1:1"}
	start := time.Now()
	if c2.Reachable(context.Background()) {
		t.Error("expected reachable=false for closed port")
	}
	if took := time.Since(start); took > 500*time.Millisecond {
		t.Errorf("reachable probe too slow: %v (cap is 250ms)", took)
	}
}

// TestDaemonClient_Fetch verifies the JSON request/response round
// trip. Server echoes a canned HTML body; client unwraps it into
// a Result with the daemon-suffixed engine.
func TestDaemonClient_Fetch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/fetch" {
			http.NotFound(w, r)
			return
		}
		var req fetchRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.URL == "" {
			http.Error(w, "url required", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(fetchResponse{
			HTML:     "<html><body>hi from " + req.URL + "</body></html>",
			FinalURL: req.URL,
			Engine:   "chromedp",
		})
	}))
	defer srv.Close()

	c := &DaemonClient{BaseURL: srv.URL}
	res, err := c.Fetch(context.Background(), "https://example.com/")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !strings.Contains(res.HTML, "hi from") {
		t.Errorf("HTML = %q", res.HTML)
	}
	if res.Engine != "chromedp+daemon" {
		t.Errorf("Engine = %q, want chromedp+daemon", res.Engine)
	}
	if res.FinalURL != "https://example.com/" {
		t.Errorf("FinalURL = %q", res.FinalURL)
	}
}

// TestDaemonClient_FetchPropagatesServerError — when the daemon
// returns a non-2xx, the client surfaces the body as part of the
// error. Important for debugging — without this the operator just
// sees "daemon: HTTP 502" with no clue what went wrong.
func TestDaemonClient_FetchPropagatesServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "chrome crashed mid-fetch", http.StatusBadGateway)
	}))
	defer srv.Close()

	c := &DaemonClient{BaseURL: srv.URL}
	_, err := c.Fetch(context.Background(), "https://example.com/")
	if err == nil {
		t.Fatal("expected error from 502")
	}
	if !strings.Contains(err.Error(), "chrome crashed") {
		t.Errorf("error missing server body: %v", err)
	}
}

// TestNewDaemonClient_Env covers the URL override knob.
func TestNewDaemonClient_Env(t *testing.T) {
	t.Run("default", func(t *testing.T) {
		c := NewDaemonClient()
		if !strings.Contains(c.BaseURL, "127.0.0.1") {
			t.Errorf("default BaseURL = %q, want loopback", c.BaseURL)
		}
	})
	t.Run("env override", func(t *testing.T) {
		t.Setenv("SOCIAL_FETCH_HEADLESS_DAEMON_URL", "https://my-headless.example.com:9000")
		c := NewDaemonClient()
		if c.BaseURL != "https://my-headless.example.com:9000" {
			t.Errorf("BaseURL = %q", c.BaseURL)
		}
	})
}
