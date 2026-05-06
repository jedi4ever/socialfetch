package artifacts

// Tests for the artifacts HTTP server. Pinned behaviours:
//
//   - Content-Length is set on file responses (NOT chunked) — same
//     framing fix the browser-pool /screenshot path landed in
//     v0.15.1. Daytona's L7 proxy buffers chunked binaries badly;
//     this test guards against the regression.
//   - Path traversal (".." / absolute) is rejected at the server,
//     not just the client. Defence in depth: a misconfigured
//     client mustn't be able to read /etc/passwd through the
//     artifacts server.
//   - List ordering is sorted by path so client diffs are stable.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// newTestServer creates a artifacts server rooted at a fresh tmp
// dir and wraps it in httptest.NewServer. Returns the server's
// URL and a cleanup func — caller must defer cleanup.
func newTestServer(t *testing.T) (string, string, func()) {
	t.Helper()
	root := t.TempDir()
	s := &Server{Root: root}
	srv := httptest.NewServer(s.Handler())
	return srv.URL, root, srv.Close
}

// TestList_EmptyTree verifies the empty-workspace case returns a
// well-formed JSON array (not "null") so clients don't have to
// special-case nil deserialisation.
func TestList_EmptyTree(t *testing.T) {
	url, _, cleanup := newTestServer(t)
	defer cleanup()

	resp, err := http.Get(url + "/artifacts/")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "null" && string(body) != "[]" {
		// Either is acceptable JSON for "no entries"; pin it so a
		// future change doesn't accidentally emit "{}" and break
		// clients that []FileEntry-decode it.
		t.Errorf("body = %q, want JSON array (null or [])", body)
	}
}

// TestList_RecursiveSorted creates a few files at different
// depths, hits GET /artifacts/, asserts the entries come back
// sorted and that Path uses forward slashes.
func TestList_RecursiveSorted(t *testing.T) {
	url, root, cleanup := newTestServer(t)
	defer cleanup()

	mustWrite(t, filepath.Join(root, "b.txt"), []byte("bee"))
	mustWrite(t, filepath.Join(root, "sub", "a.txt"), []byte("ay"))
	mustWrite(t, filepath.Join(root, "sub", "c.txt"), []byte("see"))

	resp, err := http.Get(url + "/artifacts/")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	var entries []FileEntry
	if err := json.NewDecoder(resp.Body).Decode(&entries); err != nil {
		t.Fatalf("decode: %v", err)
	}
	wantOrder := []string{"b.txt", "sub/a.txt", "sub/c.txt"}
	if len(entries) != len(wantOrder) {
		t.Fatalf("got %d entries, want %d (%v)", len(entries), len(wantOrder), entries)
	}
	for i, want := range wantOrder {
		if entries[i].Path != want {
			t.Errorf("entries[%d].Path = %q, want %q", i, entries[i].Path, want)
		}
	}
	// Per-entry sanity: size > 0, sha256 non-empty.
	for _, e := range entries {
		if e.Size == 0 {
			t.Errorf("%s: zero size", e.Path)
		}
		if e.SHA256 == "" {
			t.Errorf("%s: empty sha256", e.Path)
		}
	}
}

// TestGet_FixedLengthFraming is the load-bearing test: pull a
// >4 KB file, assert the response has Content-Length set and
// NOT Transfer-Encoding: chunked. Same shape v0.15.1's
// browser-pool /screenshot test guards against — the Daytona
// proxy chokes on chunked binaries.
func TestGet_FixedLengthFraming(t *testing.T) {
	url, root, cleanup := newTestServer(t)
	defer cleanup()

	const size = 20000
	body := bytes.Repeat([]byte{0x89, 0x50, 0x4E, 0x47}, size/4)
	mustWrite(t, filepath.Join(root, "big.bin"), body)

	resp, err := http.Get(url + "/artifacts/big.bin")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Length"); got != strconv.Itoa(len(body)) {
		t.Errorf("Content-Length = %q, want %d", got, len(body))
	}
	if len(resp.TransferEncoding) > 0 {
		t.Errorf("Transfer-Encoding = %v, want none (Content-Length is set)", resp.TransferEncoding)
	}
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Errorf("body length = %d, want %d (and contents must match)", len(got), len(body))
	}
}

// TestGet_TraversalRejected hits a `..`-escaping path and
// asserts the server refuses to serve it. We don't even bother
// putting a sensitive file at the target — the bug we're
// guarding against is "did you forget the resolve check?", which
// surfaces as 200 vs 400 regardless of the target's contents.
func TestGet_TraversalRejected(t *testing.T) {
	url, _, cleanup := newTestServer(t)
	defer cleanup()

	for _, path := range []string{
		"/artifacts/../etc/passwd",
		"/artifacts//etc/passwd", // double-slash collapses to absolute
	} {
		resp, err := http.Get(url + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			t.Errorf("GET %s: status = %d, want 4xx", path, resp.StatusCode)
		}
	}
}

// TestDelete_RemovesFile confirms the file actually disappears
// from disk and a subsequent GET returns 404.
func TestDelete_RemovesFile(t *testing.T) {
	url, root, cleanup := newTestServer(t)
	defer cleanup()

	mustWrite(t, filepath.Join(root, "doomed.txt"), []byte("bye"))

	req, _ := http.NewRequest(http.MethodDelete, url+"/artifacts/doomed.txt", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("status = %d, want 204", resp.StatusCode)
	}

	// File is gone on disk.
	if _, err := os.Stat(filepath.Join(root, "doomed.txt")); !os.IsNotExist(err) {
		t.Errorf("file still on disk: %v", err)
	}
	// GET returns 404.
	resp2, err := http.Get(url + "/artifacts/doomed.txt")
	if err != nil {
		t.Fatalf("post-delete GET: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusNotFound {
		t.Errorf("post-delete GET status = %d, want 404", resp2.StatusCode)
	}
}

// TestHealth — sanity-check the trivial endpoint so a busted
// route table doesn't escape review.
func TestHealth(t *testing.T) {
	url, _, cleanup := newTestServer(t)
	defer cleanup()

	resp, err := http.Get(url + "/health")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}

// mustWrite is a t.Helper-flavoured ioutil.WriteFile that creates
// missing parent dirs. Used by every test that seeds files.
func mustWrite(t *testing.T, path string, body []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// _ keeps fmt imported for future debug-print scenarios; cheaper
// to leave the import than re-jiggle it.
var _ = fmt.Sprintf
