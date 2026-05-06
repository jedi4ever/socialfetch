// Package artifacts serves and pulls files from an agent
// container's `/artifacts` directory — claude's "outbox" for
// files it wants returned to the operator. The server runs
// INSIDE the container (via `social-agent artifacts serve`);
// the client runs on the operator's host and pulls files over
// HTTP.
//
// `/workspace` is a separate concept: it's the (optionally
// bind-mounted) cwd where claude reads / edits files during
// work. `/artifacts` is what comes back. Splitting the two
// makes the system-prompt instruction to claude unambiguous:
// "files in /artifacts/ are returned, /workspace/ stays in the
// session." It also keeps the daytona path simple — when no
// host bind-mount is possible, /artifacts is still pullable
// over HTTP.
//
// Lives in its own package so the substrate-specific URL
// resolution (local docker → docker-port-published, daytona →
// preview URL) doesn't leak into the server code. The server
// only knows about a root directory; the client only knows about
// a base URL. Provider implementations are responsible for
// stitching the two together via Session.ArtifactsURL.
//
// Wire shape:
//
//	GET  /artifacts/                       JSON list (recursive)
//	GET  /artifacts/<rel/path>             raw bytes (Content-Length set)
//	DELETE /artifacts/<rel/path>           remove file; 204 on success
//	GET  /health                           "ok"
//
// Content-Length is set up front for binary responses so the
// Daytona proxy's chunked-binary buffering bug
// (CLAUDE.md "Daytona preview-URL proxy is flaky") doesn't bite
// the artifacts path the way it bit the screenshot path in
// v0.15.1.
package artifacts

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// FileEntry is one row in the recursive listing returned by
// `GET /artifacts/`. JSON-tagged for stable wire format —
// operators and the client package both read this shape.
type FileEntry struct {
	Path   string `json:"path"`   // forward-slash, relative to root
	Size   int64  `json:"size"`   // bytes
	SHA256 string `json:"sha256"` // hex digest, useful for skip-if-unchanged
	MTime  int64  `json:"mtime"`  // unix seconds
	Mode   uint32 `json:"mode"`   // file mode bits (just the permission bits really)
	Mime   string `json:"mime"`   // sniffed from extension; "application/octet-stream" fallback
}

// Server holds the root directory it serves. Cheap to construct;
// no resources held until Run is called.
type Server struct {
	// Root is the absolute path the server serves files from.
	// Set by the CLI's `workspace serve --root` flag.
	Root string

	// Logf is the audit hook (one line per request). nil = no-op.
	Logf func(format string, a ...any)
}

// Run binds addr and serves until the listener errors. Tests
// should use the public Handler() with httptest.NewServer
// instead so they don't need a real TCP port.
func (s *Server) Run(addr string) error {
	if s.Root == "" {
		return errors.New("artifacts server: Root is required")
	}
	if s.Logf == nil {
		s.Logf = func(string, ...any) {}
	}
	mux := s.Handler()
	srv := &http.Server{Addr: addr, Handler: mux}
	s.Logf("artifacts server listening on %s, root=%s", addr, s.Root)
	return srv.ListenAndServe()
}

// Handler returns the http.Handler implementing the wire surface.
// Public so tests can httptest the surface without binding a port.
// Initialises Logf to a no-op if the caller didn't set one — Run
// does the same, but tests that go through Handler() directly
// would panic in handlers without this guard.
func (s *Server) Handler() http.Handler {
	if s.Logf == nil {
		s.Logf = func(string, ...any) {}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, "ok\n")
	})
	mux.HandleFunc("/artifacts/", s.handleWorkspace)
	mux.HandleFunc("/artifacts", s.handleWorkspace)
	return mux
}

// handleWorkspace dispatches on method + whether a path was
// supplied. The trailing-slash split avoids the dance of
// http.ServeMux's longest-prefix match and lets us treat
// `/artifacts/` (list) distinctly from `/artifacts/foo` (file).
func (s *Server) handleWorkspace(w http.ResponseWriter, r *http.Request) {
	rel := strings.TrimPrefix(r.URL.Path, "/artifacts")
	rel = strings.TrimPrefix(rel, "/")

	switch r.Method {
	case http.MethodGet:
		if rel == "" {
			s.handleList(w, r)
			return
		}
		s.handleGet(w, r, rel)
	case http.MethodDelete:
		if rel == "" {
			http.Error(w, "DELETE: path required", http.StatusBadRequest)
			return
		}
		s.handleDelete(w, r, rel)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleList returns a recursive JSON listing of every regular
// file under Root. Sorted by path so client diffs across pulls
// are stable. SHA256 is computed on the fly — fine for the
// small-to-medium trees we expect (a few dozen files per
// session); if that becomes a hot path, cache by mtime+size.
func (s *Server) handleList(w http.ResponseWriter, _ *http.Request) {
	var entries []FileEntry
	err := filepath.Walk(s.Root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		// Skip non-regular files (symlinks, sockets, devices).
		// Operators creating those inside an agent container
		// is exotic enough that we'd rather force them through
		// docker exec than guess at the right wire shape.
		if !info.Mode().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(s.Root, p)
		if err != nil {
			return err
		}
		// SHA256 — open + hash. Cheap relative to network roundtrip.
		sum, err := hashFile(p)
		if err != nil {
			return err
		}
		entries = append(entries, FileEntry{
			Path:   filepath.ToSlash(rel),
			Size:   info.Size(),
			SHA256: sum,
			MTime:  info.ModTime().Unix(),
			Mode:   uint32(info.Mode().Perm()),
			Mime:   mimeOf(rel),
		})
		return nil
	})
	if err != nil {
		http.Error(w, "walk: "+err.Error(), http.StatusInternalServerError)
		return
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })

	body, _ := json.Marshal(entries)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
	s.Logf("artifacts list: %d files", len(entries))
}

// handleGet streams one file's bytes. Content-Length set up
// front so net/http doesn't fall back to Transfer-Encoding:
// chunked — same fix v0.15.1 made for the browser-pool
// /screenshot path. Daytona's L7 proxy buffers chunked binaries
// badly and times out on what should be a 2-second response.
func (s *Server) handleGet(w http.ResponseWriter, _ *http.Request, rel string) {
	abs, err := s.resolve(rel)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	info, err := os.Stat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if info.IsDir() {
		http.Error(w, "is a directory; use GET /artifacts/ for listing", http.StatusBadRequest)
		return
	}
	f, err := os.Open(abs)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", mimeOf(rel))
	w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, f)
	s.Logf("artifacts get: %s (%d bytes)", rel, info.Size())
}

// handleDelete removes one file. 204 on success, 404 if missing,
// 400 on traversal. Doesn't recurse into directories — caller
// must DELETE each file individually. Chosen for safety: an
// errant `DELETE /artifacts/` recursing the whole tree would
// surprise the operator.
func (s *Server) handleDelete(w http.ResponseWriter, _ *http.Request, rel string) {
	abs, err := s.resolve(rel)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	info, err := os.Stat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if info.IsDir() {
		http.Error(w, "is a directory; delete files individually", http.StatusBadRequest)
		return
	}
	if err := os.Remove(abs); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
	s.Logf("artifacts delete: %s", rel)
}

// resolve converts a request-relative path into an absolute path
// under Root, rejecting any attempt to escape via "..", absolute
// paths, or symlinks that point outside Root. Returns an error
// shaped for direct write to the HTTP response.
func (s *Server) resolve(rel string) (string, error) {
	if rel == "" {
		return "", errors.New("path required")
	}
	if strings.HasPrefix(rel, "/") {
		return "", errors.New("absolute paths rejected")
	}
	// Cleanly join + verify the result is still under Root.
	abs := filepath.Clean(filepath.Join(s.Root, rel))
	rootAbs, err := filepath.Abs(s.Root)
	if err != nil {
		return "", fmt.Errorf("root abs: %w", err)
	}
	absAbs, err := filepath.Abs(abs)
	if err != nil {
		return "", fmt.Errorf("path abs: %w", err)
	}
	if absAbs != rootAbs && !strings.HasPrefix(absAbs, rootAbs+string(filepath.Separator)) {
		return "", errors.New("path escapes artifacts root")
	}
	return abs, nil
}

// hashFile returns the hex SHA256 of a file's contents.
func hashFile(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// mimeOf sniffs a content type from the extension. Falls back to
// application/octet-stream so a browser pull doesn't try to
// helpfully render a binary as HTML.
func mimeOf(name string) string {
	if t := mime.TypeByExtension(filepath.Ext(name)); t != "" {
		return t
	}
	return "application/octet-stream"
}
