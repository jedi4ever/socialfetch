package local

// Tests that don't need real chromedp. The screenshot framing test
// is the load-bearing one — it pins the Content-Length-based
// response shape that v0.15.0 settled on after Daytona's L7 proxy
// turned out to mishandle Transfer-Encoding: chunked binary
// responses (held them until its 60s deadline → 502 "context
// deadline exceeded" on the client side).

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

// TestWriteScreenshotResponse_FixedLengthFraming verifies that
// /screenshot responses go out with Content-Length set and NOT with
// Transfer-Encoding: chunked. Use a payload >4 KB so net/http's
// auto-detect would otherwise switch to chunked — that's the regression
// path this test guards against.
func TestWriteScreenshotResponse_FixedLengthFraming(t *testing.T) {
	const size = 20000 // larger than net/http's 4 KB auto-CL threshold
	png := bytes.Repeat([]byte{0x89, 0x50, 0x4E, 0x47}, size/4)
	finalURL := "https://example.com/"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeScreenshotResponse(w, png, finalURL)
	}))
	defer srv.Close()

	resp, err := http.Post(srv.URL, "application/json", nil)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	// Content-Length must match the PNG length exactly. The Daytona
	// proxy uses this to bypass its chunked-buffering path.
	gotCL := resp.Header.Get("Content-Length")
	wantCL := strconv.Itoa(len(png))
	if gotCL != wantCL {
		t.Errorf("Content-Length = %q, want %q", gotCL, wantCL)
	}

	// resp.TransferEncoding is the parsed Transfer-Encoding header.
	// "chunked" here would mean we'd regressed back to the proxy-
	// hostile framing.
	if len(resp.TransferEncoding) > 0 {
		t.Errorf("Transfer-Encoding = %v, want none (Content-Length is set)", resp.TransferEncoding)
	}

	// The wire-format Content-Type must be image/png so the proxy
	// (and downstream clients) tag it as binary rather than text.
	if got := resp.Header.Get("Content-Type"); got != "image/png" {
		t.Errorf("Content-Type = %q, want image/png", got)
	}

	// X-Final-URL surfaces the post-redirect URL the daemon landed on.
	// social-fetch / DaemonClient reads it; missing → caller falls back
	// to the requested URL which is wrong on redirect chains.
	if got := resp.Header.Get("X-Final-Url"); got != finalURL {
		t.Errorf("X-Final-Url = %q, want %q", got, finalURL)
	}

	// Body bytes must match exactly — no truncation, no extra framing.
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(body, png) {
		t.Errorf("body length = %d, want %d (and contents must match)", len(body), len(png))
	}
}

// TestWriteScreenshotResponse_RegressionWitness pins the reason the
// helper exists: a naive handler that just calls w.Write(png) on a
// >4 KB payload triggers Transfer-Encoding: chunked. This test
// reproduces that bad shape so anyone considering "the
// Content-Length write feels redundant, let me simplify" sees the
// regression in the test name.
func TestWriteScreenshotResponse_RegressionWitness(t *testing.T) {
	const size = 20000
	png := bytes.Repeat([]byte{0x00}, size)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Deliberately NOT using writeScreenshotResponse — this is
		// what the v0.15.0-pre-fix handler did.
		w.Header().Set("Content-Type", "image/png")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(png)
	}))
	defer srv.Close()

	resp, err := http.Post(srv.URL, "application/json", nil)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()

	if got := resp.Header.Get("Content-Length"); got != "" {
		t.Errorf("control: expected NO Content-Length on chunked response, got %q", got)
	}
	if got := resp.TransferEncoding; len(got) == 0 || got[0] != "chunked" {
		t.Errorf("control: expected Transfer-Encoding: chunked, got %v — the regression test is no longer reproducing the bad shape", got)
	}
}

// TestWriteScreenshotResponse_SmallPayload confirms the framing also
// holds for tiny PNGs. Below the 4 KB auto-CL threshold net/http
// already sets Content-Length on its own, so this test just guards
// against a regression that broke the small case while fixing the
// large one.
func TestWriteScreenshotResponse_SmallPayload(t *testing.T) {
	png := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A} // PNG magic only
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeScreenshotResponse(w, png, "https://example.com/")
	}))
	defer srv.Close()

	resp, err := http.Post(srv.URL, "application/json", nil)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()

	if got := resp.Header.Get("Content-Length"); got != "8" {
		t.Errorf("Content-Length = %q, want 8", got)
	}
	if len(resp.TransferEncoding) > 0 {
		t.Errorf("Transfer-Encoding = %v, want none", resp.TransferEncoding)
	}
}
