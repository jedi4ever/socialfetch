package daytona

// workspaces_test.go pins the signed-preview-URL contract.
// Daytona's standard preview URL form
// (`<port>-<full-uuid>.daytonaproxy01.net` + bearer token via
// `X-Daytona-Preview-Token`) is intermittently routed through
// Auth0's PKCE OAuth flow, returning 307 → 404 → cached at the CF
// edge for 60s. The signed form
// (`<port>-<short-token-id>.daytonaproxy01.net`) bypasses that
// path entirely. If a future refactor accidentally drops the
// `signed-preview-url` path or starts attaching the token as a
// header, this test fails fast.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGetPreviewURL_HitsSignedEndpointAndDropsToken(t *testing.T) {
	var seenPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenPath = r.URL.Path + "?" + r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"sandboxId": "fake-id",
			"port":      5556,
			"url":       "https://5556-shortid123.daytonaproxy01.net",
			// API returns a token field, but we expect
			// GetPreviewURL to discard it so the daemon doesn't
			// attach it as a bearer header (which would trigger
			// the Auth0 redirect on the signed-URL path).
			"token": "should-be-discarded",
		})
	}))
	defer srv.Close()

	c := &Client{
		BaseURL: srv.URL,
		APIKey:  "fake",
		OrgID:   "fake",
		HTTP:    http.DefaultClient,
	}

	got, err := c.GetPreviewURL(context.Background(), "fake-id", 5556, 3600)
	if err != nil {
		t.Fatalf("GetPreviewURL: %v", err)
	}

	if !strings.Contains(seenPath, "/sandbox/fake-id/ports/5556/signed-preview-url") {
		t.Errorf("hit %q, expected /sandbox/<id>/ports/<port>/signed-preview-url", seenPath)
	}
	if !strings.Contains(seenPath, "expires=3600") {
		t.Errorf("expires param missing in %q", seenPath)
	}
	if got.URL != "https://5556-shortid123.daytonaproxy01.net" {
		t.Errorf("URL = %q, want signed-form URL", got.URL)
	}
	if got.Token != "" {
		t.Errorf("Token = %q, want empty (auth is in URL — header attachment must NOT happen)", got.Token)
	}
}

func TestGetPreviewURL_OmitsExpiresWhenZero(t *testing.T) {
	var seenQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"url":"https://5556-x.daytonaproxy01.net"}`))
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL, APIKey: "k", OrgID: "o", HTTP: http.DefaultClient}
	if _, err := c.GetPreviewURL(context.Background(), "id", 5556, 0); err != nil {
		t.Fatal(err)
	}
	if seenQuery != "" {
		t.Errorf("expected empty query when expiresSec=0, got %q", seenQuery)
	}
}
