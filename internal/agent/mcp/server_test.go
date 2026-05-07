package mcp

// server_test.go covers the per-run-session output directory used
// by social_agent_run to stash artifacts before container teardown.
// Without this the streaming-mode one-shot path would emit artifact
// metadata events that point at files no longer reachable by the
// time the response lands client-side.

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jedi4ever/social-skills/internal/agent/streaming"
)

func TestNewSessionDir_CreatesSubdirsAndIsUnique(t *testing.T) {
	rootA, artA, inA, err := newSessionDir()
	if err != nil {
		t.Fatalf("first newSessionDir: %v", err)
	}
	defer os.RemoveAll(rootA)

	if _, err := os.Stat(rootA); err != nil {
		t.Errorf("session root %q does not exist: %v", rootA, err)
	}
	if _, err := os.Stat(artA); err != nil {
		t.Errorf("artifacts dir %q does not exist: %v", artA, err)
	}
	if _, err := os.Stat(inA); err != nil {
		t.Errorf("inputs dir %q does not exist: %v", inA, err)
	}
	if filepath.Base(artA) != "artifacts" {
		t.Errorf("artifacts dir base = %q, want \"artifacts\"", filepath.Base(artA))
	}
	if filepath.Base(inA) != "inputs" {
		t.Errorf("inputs dir base = %q, want \"inputs\"", filepath.Base(inA))
	}
	if filepath.Dir(artA) != rootA || filepath.Dir(inA) != rootA {
		t.Errorf("subdirs not under root: art parent=%q in parent=%q root=%q", filepath.Dir(artA), filepath.Dir(inA), rootA)
	}

	want := filepath.Join(os.TempDir(), "social-agent")
	if !strings.HasPrefix(rootA, want+string(os.PathSeparator)) {
		t.Errorf("session root %q does not live under %q", rootA, want)
	}

	rootB, _, _, err := newSessionDir()
	if err != nil {
		t.Fatalf("second newSessionDir: %v", err)
	}
	defer os.RemoveAll(rootB)

	if rootA == rootB {
		t.Errorf("two consecutive calls collided on %q — should have a unique random suffix", rootA)
	}
}

func TestStageInputs_CopiesFilesAndRejectsDirs(t *testing.T) {
	tmp := t.TempDir()
	src1 := filepath.Join(tmp, "notes.md")
	src2 := filepath.Join(tmp, "spec.txt")
	dirSrc := filepath.Join(tmp, "subdir")
	if err := os.WriteFile(src1, []byte("# notes"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src2, []byte("spec body"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dirSrc, 0o755); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(tmp, "inputs")
	if err := os.Mkdir(dest, 0o755); err != nil {
		t.Fatal(err)
	}

	staged, err := stageInputs([]string{src1, src2}, dest)
	if err != nil {
		t.Fatalf("stageInputs: %v", err)
	}
	if len(staged) != 2 {
		t.Errorf("staged %d files, want 2", len(staged))
	}
	if got, _ := os.ReadFile(filepath.Join(dest, "notes.md")); string(got) != "# notes" {
		t.Errorf("notes.md content = %q", string(got))
	}

	if _, err := stageInputs([]string{dirSrc}, dest); err == nil {
		t.Errorf("expected error when staging a directory")
	}

	if _, err := stageInputs([]string{filepath.Join(tmp, "missing.bin")}, dest); err == nil {
		t.Errorf("expected error for missing file")
	}
}

func TestProgressSummary_HumanReadable(t *testing.T) {
	cases := []struct {
		name string
		e    streaming.Event
		want string
	}{
		{"text trims whitespace", streaming.Event{Kind: "text", Content: "  hello world  "}, "hello world"},
		{"text empty → skip", streaming.Event{Kind: "text", Content: "   "}, ""},
		{"artifact with size + mime", streaming.Event{Kind: "artifact", Path: "test.md", Size: 158, Mime: "text/markdown"}, "wrote test.md (158 bytes, text/markdown)"},
		{"session up shortens id", streaming.Event{Kind: "session", Status: "up", ID: "5023c54a4c9a35f43c0916170a83bf2780d2dfe54ad8f333c400ca510c5f4e7c"}, "session up: 5023c54a4c9a"},
		{"session down", streaming.Event{Kind: "session", Status: "down", ID: "abc123"}, "session down: abc123"},
		{"done ok", streaming.Event{Kind: "done", ExitCode: 0}, "done (exit 0)"},
		{"done with error", streaming.Event{Kind: "done", ExitCode: 1, Error: "boom"}, "done with error: boom"},
		{"error event", streaming.Event{Kind: "error", Error: "thing failed"}, "error: thing failed"},
		{"claude_event noise → skip", streaming.Event{Kind: "claude_event"}, ""},
		{"unknown kind → skip", streaming.Event{Kind: "future-kind"}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := progressSummary(c.e); got != c.want {
				t.Errorf("progressSummary(%+v) = %q, want %q", c.e, got, c.want)
			}
		})
	}
}

func TestProgressSummary_TextTruncates(t *testing.T) {
	long := strings.Repeat("a", 200)
	got := progressSummary(streaming.Event{Kind: "text", Content: long})
	if len(got) != 160 {
		t.Errorf("long text length = %d, want 160 (157 + \"...\")", len(got))
	}
	if !strings.HasSuffix(got, "...") {
		t.Errorf("long text not truncated with ellipsis: %q", got)
	}
}

func TestNewSessionDir_NameShape(t *testing.T) {
	root, _, _, err := newSessionDir()
	if err != nil {
		t.Fatalf("newSessionDir: %v", err)
	}
	defer os.RemoveAll(root)

	base := filepath.Base(root)
	// Format: 20060102T150405-<8 hex> = 15 + 1 + 8 = 24 chars.
	if len(base) != 24 {
		t.Errorf("base name %q length = %d, want 24 (timestamp-hex)", base, len(base))
	}
	if base[15] != '-' {
		t.Errorf("base name %q missing '-' separator at index 15", base)
	}
}

// TestIsPrintableUTF8 pins down the boundary between text and binary
// payloads for social_agent_read. The tool routes text through the
// `content` field and binary through `content_b64`; misclassifying
// either way breaks downstream rendering.
func TestIsPrintableUTF8(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
		want bool
	}{
		{"empty", []byte{}, true},
		{"ascii", []byte("hello world\n"), true},
		{"utf8 multibyte", []byte("héllo — 中文 ✓"), true},
		{"contains NUL", []byte("hello\x00world"), false},
		{"invalid utf8 sequence", []byte{0xff, 0xfe, 0xfd}, false},
		{"png magic bytes", []byte{0x89, 0x50, 0x4e, 0x47}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isPrintableUTF8(tc.in); got != tc.want {
				t.Fatalf("isPrintableUTF8(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// TestBase64StdEncode is a lightweight pin against accidentally
// switching to URL-safe base64 — Claude clients we care about
// expect StdEncoding.
func TestBase64StdEncode(t *testing.T) {
	got := base64StdEncode([]byte{0xff, 0xfe, 0xfd})
	if got != "//79" {
		t.Fatalf("base64StdEncode = %q, want %q", got, "//79")
	}
}

// TestSafeWorkspacePath locks in the path-traversal defence for
// social_agent_download / future tools that resolve caller-supplied
// paths against the workspace dir. A regression here would let a
// hostile prompt read arbitrary files off the MCP-server's disk.
func TestSafeWorkspacePath(t *testing.T) {
	root := t.TempDir()
	cases := []struct {
		name    string
		rel     string
		wantErr bool
	}{
		{"plain file", "report.md", false},
		{"nested", "subdir/file.json", false},
		{"current dir", ".", false},
		{"single dotdot", "..", true},
		{"dotdot prefix", "../etc/passwd", true},
		{"absolute path", "/etc/passwd", true},
		{"sneaky dotdot in middle", "ok/../../escape", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := safeWorkspacePath(root, tc.rel)
			if tc.wantErr && err == nil {
				t.Fatalf("safeWorkspacePath(%q) returned no error, want one", tc.rel)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("safeWorkspacePath(%q) returned error %v, want nil", tc.rel, err)
			}
		})
	}
}

// TestEnsureFreshArtifacts confirms that the wipe-before-run hook
// removes prior contents but preserves the dir itself (so the
// container's bind-mount target stays valid).
func TestEnsureFreshArtifacts(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"old1.txt", "old2.txt"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("stale"), 0o644); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	if err := os.Mkdir(filepath.Join(root, "subdir"), 0o755); err != nil {
		t.Fatalf("seed subdir: %v", err)
	}
	if err := ensureFreshArtifacts(root); err != nil {
		t.Fatalf("ensureFreshArtifacts: %v", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected empty dir after wipe, got %d entries", len(entries))
	}
	if _, err := os.Stat(root); err != nil {
		t.Errorf("dir itself was removed: %v", err)
	}
}

// TestNewSessionID covers the format invariant — 64 hex chars, no
// timestamp prefix. Underpins the access-control story: a caller
// without the session_id can't enumerate or guess one.
func TestNewSessionID(t *testing.T) {
	for i := 0; i < 5; i++ {
		got := newSessionID()
		if len(got) != 64 {
			t.Errorf("newSessionID len = %d, want 64", len(got))
		}
		if !validSessionID(got) {
			t.Errorf("newSessionID returned %q which validSessionID rejects", got)
		}
	}
	// Uniqueness check — two calls in a row shouldn't collide.
	if a, b := newSessionID(), newSessionID(); a == b {
		t.Errorf("two newSessionID calls collided: %q == %q", a, b)
	}
}

// TestValidSessionID locks down the allowlist: lowercase hex,
// exactly 64 chars. Rejecting anything else is a path-traversal
// guard for sessionDirs / session_close.
func TestValidSessionID(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"valid 64 hex", strings.Repeat("0123456789abcdef", 4), true},
		{"too short", "abc123", false},
		{"too long", strings.Repeat("a", 65), false},
		{"uppercase rejected", strings.Repeat("ABCDEF0123456789", 4), false},
		{"contains slash", strings.Repeat("a", 32) + "/" + strings.Repeat("b", 31), false},
		{"contains dotdot", "..%" + strings.Repeat("a", 60), false},
		{"empty", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := validSessionID(tc.in); got != tc.want {
				t.Errorf("validSessionID(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// TestSessionDirs_RoundTrip — create a session, look it up, close
// it, confirm the lookup fails. Mirrors the lifecycle the MCP
// tools exercise.
func TestSessionDirs_RoundTrip(t *testing.T) {
	// Redirect $TMPDIR for the test so we don't pollute real /tmp
	// or interfere with other tests that may share $TMPDIR.
	t.Setenv("TMPDIR", t.TempDir())

	id := newSessionID()
	if _, _, err := sessionDirs(id); err == nil {
		t.Fatalf("sessionDirs returned no error for non-existent session")
	}
	inputs, artifacts, err := createSessionDirs(id)
	if err != nil {
		t.Fatalf("createSessionDirs: %v", err)
	}
	if filepath.Base(inputs) != "inputs" || filepath.Base(artifacts) != "artifacts" {
		t.Errorf("dirs: inputs=%q artifacts=%q", inputs, artifacts)
	}
	if _, err := os.Stat(inputs); err != nil {
		t.Errorf("inputs dir not created: %v", err)
	}
	if _, err := os.Stat(artifacts); err != nil {
		t.Errorf("artifacts dir not created: %v", err)
	}
	if got, _, err := sessionDirs(id); err != nil || got != inputs {
		t.Errorf("sessionDirs(existing) returned (%q, %v); want (%q, nil)", got, err, inputs)
	}
	// Simulate session_close: rm -rf the session root.
	if err := os.RemoveAll(filepath.Join(sessionsRoot(), id)); err != nil {
		t.Fatalf("rm session: %v", err)
	}
	if _, _, err := sessionDirs(id); err == nil {
		t.Errorf("sessionDirs returned no error after close")
	}
}

// TestArtifactsHandler exercises the HTTP path that backs the
// `url` field in list_artifacts responses. Verifies the happy
// path serves bytes, malformed sessions / paths get rejected, and
// path-traversal attempts get a 403.
func TestArtifactsHandler(t *testing.T) {
	// Spin up a fake session inside a TempDir-backed sessionsRoot.
	// The handler reads from $TMPDIR/social-agent/sessions/<id>/...
	// so set TMPDIR to a fresh dir for this test only.
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	sessionID := newSessionID()
	_, artDir, err := createSessionDirs(sessionID)
	if err != nil {
		t.Fatalf("createSessionDirs: %v", err)
	}
	body := []byte("hello, artifact\n")
	if err := os.WriteFile(filepath.Join(artDir, "report.md"), body, 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	h := NewArtifactsHandler()

	// Happy path.
	rec := httptestRecord(t, h, "GET", ArtifactsURLPrefix+sessionID+"/report.md")
	if rec.code != 200 {
		t.Errorf("happy: got %d, want 200", rec.code)
	}
	if string(rec.body) != string(body) {
		t.Errorf("happy: body %q, want %q", rec.body, body)
	}

	// Wrong session id format.
	rec = httptestRecord(t, h, "GET", ArtifactsURLPrefix+"badsession/report.md")
	if rec.code != 400 {
		t.Errorf("bad session_id: got %d, want 400", rec.code)
	}

	// Unknown session id (well-formed but not on disk).
	rec = httptestRecord(t, h, "GET", ArtifactsURLPrefix+strings.Repeat("a", 64)+"/report.md")
	if rec.code != 404 {
		t.Errorf("unknown session: got %d, want 404", rec.code)
	}

	// Path traversal.
	rec = httptestRecord(t, h, "GET", ArtifactsURLPrefix+sessionID+"/../../etc/passwd")
	if rec.code != 403 && rec.code != 400 {
		t.Errorf("traversal: got %d, want 403 or 400", rec.code)
	}

	// Wrong method.
	rec = httptestRecord(t, h, "POST", ArtifactsURLPrefix+sessionID+"/report.md")
	if rec.code != 405 {
		t.Errorf("POST: got %d, want 405", rec.code)
	}
}

// httptestRecord runs a request against h and returns the recorded
// response. Tiny inlined helper to keep the test free of an
// httptest dep on the package surface.
type recordedResponse struct {
	code int
	body []byte
	hdr  http.Header
}

func httptestRecord(t *testing.T, h http.Handler, method, path string) recordedResponse {
	t.Helper()
	req, err := http.NewRequest(method, "http://example"+path, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	rw := &recordingResponseWriter{hdr: http.Header{}}
	h.ServeHTTP(rw, req)
	return recordedResponse{code: rw.code, body: rw.body, hdr: rw.hdr}
}

type recordingResponseWriter struct {
	code int
	hdr  http.Header
	body []byte
}

func (r *recordingResponseWriter) Header() http.Header { return r.hdr }
func (r *recordingResponseWriter) Write(p []byte) (int, error) {
	if r.code == 0 {
		r.code = 200
	}
	r.body = append(r.body, p...)
	return len(p), nil
}
func (r *recordingResponseWriter) WriteHeader(code int) { r.code = code }

// TestListArtifacts walks a fixture tree and verifies the expected
// {path, size} entries come back, with forward-slash separators.
func TestListArtifacts(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatalf("seed a: %v", err)
	}
	sub := filepath.Join(root, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir sub: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sub, "b.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatalf("seed b: %v", err)
	}
	entries, err := listArtifacts(root)
	if err != nil {
		t.Fatalf("listArtifacts: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2: %+v", len(entries), entries)
	}
	gotPaths := map[string]int64{}
	for _, e := range entries {
		gotPaths[e.Path] = e.Size
	}
	if gotPaths["a.txt"] != 2 {
		t.Errorf("a.txt size: got %d, want 2", gotPaths["a.txt"])
	}
	if gotPaths["sub/b.txt"] != 5 {
		t.Errorf("sub/b.txt size: got %d, want 5; got entries=%+v", gotPaths["sub/b.txt"], entries)
	}
}
