package ledger

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestDaemonClient_Reachable — quick probe returns true when /status
// answers with 200, false within the cap when the port is closed.
func TestDaemonClient_Reachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/status" {
			w.WriteHeader(200)
			_ = json.NewEncoder(w).Encode(StatusResponse{DBPath: "/tmp/x"})
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	c := &DaemonClient{BaseURL: srv.URL}
	if !c.Reachable(context.Background()) {
		t.Error("expected reachable=true")
	}

	closed := &DaemonClient{BaseURL: "http://127.0.0.1:1"}
	start := time.Now()
	if closed.Reachable(context.Background()) {
		t.Error("expected reachable=false for closed port")
	}
	if took := time.Since(start); took > 500*time.Millisecond {
		t.Errorf("probe too slow: %v (cap is 250ms)", took)
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
		t.Setenv("SOCIAL_LEDGER_DAEMON_URL", "https://my-ledger.example.com:9000")
		c := NewDaemonClient()
		if c.BaseURL != "https://my-ledger.example.com:9000" {
			t.Errorf("BaseURL = %q", c.BaseURL)
		}
	})
}

// TestDisabled — env var flips Disabled() so callers can short-
// circuit before the reachability probe.
func TestDisabled(t *testing.T) {
	t.Run("unset", func(t *testing.T) {
		t.Setenv("SOCIAL_LEDGER_DAEMON_DISABLE", "")
		if Disabled() {
			t.Error("Disabled() = true with empty env")
		}
	})
	t.Run("set to 1", func(t *testing.T) {
		t.Setenv("SOCIAL_LEDGER_DAEMON_DISABLE", "1")
		if !Disabled() {
			t.Error("Disabled() = false with var set")
		}
	})
	t.Run("set to whitespace stays disabled-off", func(t *testing.T) {
		t.Setenv("SOCIAL_LEDGER_DAEMON_DISABLE", "   ")
		if Disabled() {
			t.Error("whitespace value should not flip Disabled")
		}
	})
}

// TestDaemonClient_ContentURL builds the URL MCP hands to remote
// agents — locks in the format so the URL stays parseable + the
// `url` query param is properly encoded.
func TestDaemonClient_ContentURL(t *testing.T) {
	c := &DaemonClient{BaseURL: "http://127.0.0.1:5557"}
	got := c.ContentURL("https://news.ycombinator.com/item?id=42")
	want := "http://127.0.0.1:5557/content?url=https%3A%2F%2Fnews.ycombinator.com%2Fitem%3Fid%3D42"
	if got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

// TestDaemonClient_StatusError — when the daemon returns non-2xx,
// the client surfaces the body in the error so the operator
// debugging knows what went wrong.
func TestDaemonClient_StatusError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "ledger db is locked", http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	c := &DaemonClient{BaseURL: srv.URL}
	_, err := c.Status(context.Background())
	if err == nil {
		t.Fatal("expected error from 503")
	}
	if !strings.Contains(err.Error(), "503") {
		t.Errorf("error = %v, want it to mention HTTP 503", err)
	}
}
