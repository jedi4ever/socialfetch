package xauth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestBearerTokenExchangesAndCaches(t *testing.T) {
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodPost {
			t.Errorf("want POST, got %s", r.Method)
		}
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Basic ") {
			t.Errorf("missing Basic auth: %q", auth)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"token_type":"bearer","access_token":"abc123"}`))
	}))
	defer srv.Close()

	prev := TokenURL
	TokenURL = srv.URL
	defer func() { TokenURL = prev }()
	ResetCache()

	ctx := context.Background()
	c := Credentials{Key: "k", Secret: "s"}
	tok, err := BearerToken(ctx, c)
	if err != nil || tok != "abc123" {
		t.Fatalf("got %q, %v", tok, err)
	}

	// Second call should be cached — no extra network hit.
	tok2, err := BearerToken(ctx, c)
	if err != nil || tok2 != "abc123" {
		t.Errorf("cache miss: %q, %v", tok2, err)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("token endpoint hit %d times; want 1 (second call should be cached)", got)
	}
}

func TestBearerTokenRequiresCreds(t *testing.T) {
	if _, err := BearerToken(context.Background(), Credentials{}); err == nil {
		t.Errorf("expected error for empty creds")
	}
}

func TestFromEnv(t *testing.T) {
	t.Setenv("X_API_KEY", "")
	t.Setenv("X_API_SECRET", "")
	if _, ok := FromEnv(); ok {
		t.Errorf("want ok=false when unset")
	}
	t.Setenv("X_API_KEY", "k")
	t.Setenv("X_API_SECRET", "s")
	c, ok := FromEnv()
	if !ok || c.Key != "k" || c.Secret != "s" {
		t.Errorf("got %+v ok=%v", c, ok)
	}
}
