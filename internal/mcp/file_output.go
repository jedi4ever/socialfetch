package mcp

// File-based output for MCP tools whose responses can grow large
// (article bodies, research reports, full ledger entries). Bouncing a
// 50 KB markdown body through MCP's stdio JSON-RPC channel is slow on
// both ends — server side has to JSON-string-escape every newline /
// quote, the client has to allocate + parse the same blob, and the
// LLM ends up with the entire body in its context whether it needed
// it or not.
//
// The fix: write the body to a temp file once, return a small envelope
// (metadata + path + size). Two read paths from there:
//
//  1. Claude Code agents call the built-in Read tool on `content_file`.
//     That goes through the Claude Code filesystem path, not MCP, so
//     no JSON escape cost.
//
//  2. Claude Desktop agents (no built-in Read) call the
//     social_fetch_read_file MCP tool — it pages the file in chunks so
//     the agent can stop early once it has what it needs.
//
// Either way the LLM only sees the small envelope unless it explicitly
// asks for the body. That alone shaves a lot of context-processing
// latency off Claude Desktop calls.
//
// Tools opt back into the old inline shape with `inline: true`.

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// writeContentTemp persists body to a temp file under os.TempDir().
// Naming pattern: `social-fetch-<tool>-*.<ext>` so the user can
// identify orphan files when poking at /tmp.
//
// We deliberately don't clean up — Linux's /tmp gets wiped on reboot,
// macOS auto-GCs /var/folders/xxx. The agent reads each file once via
// its Read tool (or social_fetch_read_file in MCP-only clients), then
// forgets it.
func writeContentTemp(tool, ext, body string) (string, int, error) {
	if ext == "" {
		ext = "md"
	}
	f, err := os.CreateTemp("", "social-fetch-"+tool+"-*."+ext)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	n, err := f.WriteString(body)
	if err != nil {
		os.Remove(f.Name())
		return "", 0, err
	}
	return f.Name(), n, nil
}

// ---- read_file tool --------------------------------------------------

type readFileArgs struct {
	Path     string `json:"path"`
	Start    int    `json:"start_byte,omitempty"`
	MaxBytes int    `json:"max_bytes,omitempty"`
}

// addReadFileTool registers `social_fetch_read_file`, the chunked
// reader for content files produced by other tools. Path is locked to
// os.TempDir() with the `social-fetch-` prefix so this tool can't be
// turned into a generic filesystem reader by a misbehaving caller.
func addReadFileTool(s *server.MCPServer, cfg Config) {
	tool := mcp.NewTool("social_fetch_read_file",
		mcp.WithDescription("Read a content file produced by an earlier social_fetch_* / social_ledger_* call (returned as `content_file` in the tool result). For Claude Code agents the built-in Read tool is the faster path; this MCP tool exists for clients that don't have a filesystem Read (Claude Desktop). Returns up to 64 KB per call by default; use `start_byte` + the returned `next_start` to page through larger files. Path is locked to the system temp dir + `social-fetch-` prefix."),
		mcp.WithString("path", mcp.Required(), mcp.Description("Absolute file path from a previous tool's `content_file` field. Must live under os.TempDir() and start with `social-fetch-`.")),
		mcp.WithNumber("start_byte", mcp.Description("Byte offset to start reading at (default 0). Use the previous response's `next_start` to resume.")),
		mcp.WithNumber("max_bytes", mcp.Description("Max bytes to return (default 65536, max 1048576). Cap exists to keep the MCP response bounded.")),
	)
	s.AddTool(tool, mcp.NewTypedToolHandler(func(ctx context.Context, _ mcp.CallToolRequest, args readFileArgs) (*mcp.CallToolResult, error) {
		audit, closeAudit := openToolAudit(cfg, "read_file")
		defer closeAudit()
		if strings.TrimSpace(args.Path) == "" {
			return mcp.NewToolResultError("path is required"), nil
		}
		clean, err := safeTempPath(args.Path)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		f, err := os.Open(clean)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		start := int64(args.Start)
		if start < 0 {
			start = 0
		}
		if start > info.Size() {
			start = info.Size()
		}
		if _, err := f.Seek(start, io.SeekStart); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		n := args.MaxBytes
		switch {
		case n <= 0:
			n = 64 * 1024
		case n > 1024*1024:
			n = 1024 * 1024
		}
		buf := make([]byte, n)
		read, err := io.ReadFull(f, buf)
		if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
			return mcp.NewToolResultError(err.Error()), nil
		}
		audit.Logf("read_file %s start=%d bytes=%d total=%d", clean, start, read, info.Size())
		nextStart := start + int64(read)
		eof := nextStart >= info.Size()
		return jsonResult(map[string]any{
			"path":       clean,
			"size":       info.Size(),
			"start":      start,
			"bytes":      read,
			"next_start": nextStart,
			"eof":        eof,
			"content":    string(buf[:read]),
		})
	}))
}

// safeTempPath locks the read tool down to files this server itself
// produced — `os.TempDir()` prefix + the `social-fetch-` filename
// prefix used by writeContentTemp. Anything else gets rejected so
// `social_fetch_read_file` can't be turned into a generic
// arbitrary-path file reader by a hostile prompt.
func safeTempPath(p string) (string, error) {
	clean := filepath.Clean(p)
	td, err := filepath.EvalSymlinks(filepath.Clean(os.TempDir()))
	if err != nil {
		td = filepath.Clean(os.TempDir())
	}
	// Resolve symlinks on the input too so e.g. /tmp -> /private/tmp on
	// macOS doesn't reject a perfectly valid path.
	resolved, err := filepath.EvalSymlinks(clean)
	if err == nil {
		clean = resolved
	}
	if !strings.HasPrefix(clean, td+string(filepath.Separator)) {
		return "", fmt.Errorf("path must be under %s", td)
	}
	base := filepath.Base(clean)
	if !strings.HasPrefix(base, "social-fetch-") {
		return "", fmt.Errorf("filename must start with social-fetch-")
	}
	return clean, nil
}
