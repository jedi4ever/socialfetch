package core

import (
	"net/http"
	"strings"
	"testing"
)

func TestIsCloudflareBlocked(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		headers map[string]string
		body    string
		want    bool
	}{
		{
			name:    "cf-mitigated challenge — explicit signal",
			status:  http.StatusForbidden,
			headers: map[string]string{"cf-mitigated": "challenge"},
			body:    "",
			want:    true,
		},
		{
			name:    "cf-ray + 403 + challenge body",
			status:  http.StatusForbidden,
			headers: map[string]string{"cf-ray": "9f56eca22b9e560d-BRU", "server": "cloudflare"},
			body:    `<html><head><title>Just a moment...</title></head><body><div id="cf-error-details"></div></body></html>`,
			want:    true,
		},
		{
			name:    "cf-ray + 503 + challenge token",
			status:  http.StatusServiceUnavailable,
			headers: map[string]string{"cf-ray": "abc"},
			body:    `<script src="/cdn-cgi/challenge-platform/h/g/orchestrate/chl_page/v1?ray=abc"></script>`,
			want:    true,
		},
		{
			name:    "cf-ray + 200 — not a challenge, the page came through",
			status:  http.StatusOK,
			headers: map[string]string{"cf-ray": "abc", "server": "cloudflare"},
			body:    "<html>real content</html>",
			want:    false,
		},
		{
			name:    "no CF headers + challenge-shaped body — could be anything",
			status:  http.StatusForbidden,
			headers: nil,
			body:    "Just a moment please",
			want:    false,
		},
		{
			name:    "real 403 from CF-fronted origin (auth wall)",
			status:  http.StatusForbidden,
			headers: map[string]string{"cf-ray": "abc"},
			body:    `{"error": "authentication required"}`,
			want:    false,
		},
		{
			name:    "401 auth — never a CF challenge",
			status:  http.StatusUnauthorized,
			headers: map[string]string{"cf-mitigated": "challenge"},
			body:    "",
			want:    false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := &http.Response{StatusCode: tc.status, Header: http.Header{}}
			for k, v := range tc.headers {
				resp.Header.Set(k, v)
			}
			got := IsCloudflareBlocked(resp, []byte(tc.body))
			if got != tc.want {
				t.Errorf("IsCloudflareBlocked() = %v, want %v (status=%d body=%q)",
					got, tc.want, tc.status, truncate(tc.body, 60))
			}
		})
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// Sanity check that the markers list is lowercase — the function
// lowercases the body before matching, so any uppercase marker would
// never match.
func TestCloudflareMarkersAreLowercase(t *testing.T) {
	for _, m := range cfChallengeMarkers {
		if string(m) != strings.ToLower(string(m)) {
			t.Errorf("marker %q has uppercase chars", m)
		}
	}
}
