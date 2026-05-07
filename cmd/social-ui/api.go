package main

// api.go — REST handlers + a tiny inline MCP client.
//
// Route shape:
//
//   POST   /api/sessions                       create   → {session_id}
//   DELETE /api/sessions/{sid}                 close
//   POST   /api/sessions/{sid}/runs            new run  → {run_id}
//   GET    /api/sessions/{sid}/runs/{rid}      run_status
//
// Each handler builds a JSON-RPC tools/call request against the
// host social-agent MCP, unwraps the typed-text result, returns
// it to the browser as plain JSON. No streaming — the frontend
// polls run_status until done. Polling is plenty at single-
// operator scale; SSE is a v2 if the polling load ever matters.
//
// MCP transport here is the Streamable HTTP shape mcp-go's
// server exposes. Our client manages its own session id (the
// `Mcp-Session-Id` header) per process, established once on
// initialize and reused across calls.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	uiweb "github.com/jedi4ever/social-skills/cmd/social-ui/web"
)

type api struct {
	agentURL  string
	bearer    string
	httpc     *http.Client
	sessionMu sync.Mutex
	sessionID string
	rpcID     int
}

func newAPI(agentURL, bearer string) *api {
	return &api{
		agentURL: strings.TrimRight(agentURL, "/"),
		bearer:   bearer,
		httpc: &http.Client{
			Timeout: 60 * time.Second,
		},
	}
}

// ---- index ------------------------------------------------------

func (a *api) serveIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	data, err := uiweb.Files.ReadFile("static/index.html")
	if err != nil {
		http.Error(w, "index.html missing from embed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(data)
}

// ---- health -----------------------------------------------------

func (a *api) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"service":"social-ui","status":"ok"}`))
}

// ---- /api/sessions (collection) ---------------------------------

func (a *api) handleSessionsCollection(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		a.createSession(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (a *api) createSession(w http.ResponseWriter, r *http.Request) {
	out, err := a.callTool(r.Context(), "social_agent_session_create", map[string]any{})
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// ---- /api/sessions/{sid}/... ------------------------------------

func (a *api) handleSessionsItem(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/sessions/")
	if rest == "" {
		http.Error(w, "missing session id", http.StatusBadRequest)
		return
	}
	parts := strings.Split(rest, "/")
	sid := parts[0]
	switch {
	case len(parts) == 1 && r.Method == http.MethodDelete:
		a.closeSession(w, r, sid)
	case len(parts) == 2 && parts[1] == "runs" && r.Method == http.MethodPost:
		a.startRun(w, r, sid)
	case len(parts) == 3 && parts[1] == "runs" && r.Method == http.MethodGet:
		a.runStatus(w, r, sid, parts[2])
	default:
		http.Error(w, "no route for "+r.Method+" "+r.URL.Path, http.StatusNotFound)
	}
}

func (a *api) closeSession(w http.ResponseWriter, r *http.Request, sid string) {
	out, err := a.callTool(r.Context(), "social_agent_session_close", map[string]any{
		"session_id": sid,
	})
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *api) startRun(w http.ResponseWriter, r *http.Request, sid string) {
	var body struct {
		Prompt string `json:"prompt"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(body.Prompt) == "" {
		http.Error(w, "prompt required", http.StatusBadRequest)
		return
	}
	out, err := a.callTool(r.Context(), "social_agent_run", map[string]any{
		"session_id": sid,
		"prompt":     body.Prompt,
	})
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *api) runStatus(w http.ResponseWriter, r *http.Request, sid, rid string) {
	_ = sid // run_id is globally unique; sid is in the URL only for client-side correlation
	out, err := a.callTool(r.Context(), "social_agent_run_status", map[string]any{
		"run_id": rid,
	})
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// ---- MCP call helper --------------------------------------------

// callTool issues a JSON-RPC tools/call against the host
// social-agent MCP and returns the parsed text-content payload as
// a generic map. Lazy-initializes the MCP session on first call
// (sends initialize, captures the Mcp-Session-Id, then sends
// notifications/initialized).
//
// All this binary's tool calls reuse the one MCP session so the
// host server's session bookkeeping doesn't churn. Re-initialize
// on session-id-mismatch errors (e.g. host MCP restarted).
func (a *api) callTool(ctx context.Context, name string, args map[string]any) (map[string]any, error) {
	if err := a.ensureMCPSession(ctx); err != nil {
		return nil, fmt.Errorf("mcp init: %w", err)
	}
	id := a.nextRPCID()
	body := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      name,
			"arguments": args,
		},
	}
	respBody, _, err := a.doMCP(ctx, body)
	if err != nil {
		return nil, err
	}
	// Parse: {result: {content: [{type: "text", text: "<json>"}]}}
	var env struct {
		Result struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(respBody, &env); err != nil {
		return nil, fmt.Errorf("decode mcp response: %w (body: %s)", err, snippet(respBody))
	}
	if env.Error != nil {
		return nil, fmt.Errorf("mcp error %d: %s", env.Error.Code, env.Error.Message)
	}
	if len(env.Result.Content) == 0 {
		return nil, fmt.Errorf("mcp result empty (raw: %s)", snippet(respBody))
	}
	first := env.Result.Content[0]
	if first.Type != "text" {
		return nil, fmt.Errorf("mcp result type %q (want text)", first.Type)
	}
	if env.Result.IsError {
		// The text payload IS the error message — surface verbatim.
		return nil, fmt.Errorf("%s", first.Text)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(first.Text), &out); err != nil {
		// Tool returned plain text, not JSON. Wrap so the caller
		// still gets something structured.
		return map[string]any{"text": first.Text}, nil
	}
	return out, nil
}

// ensureMCPSession does the initialize handshake on first use.
// Subsequent calls are no-ops. Cheap one-time setup; not a goroutine.
func (a *api) ensureMCPSession(ctx context.Context) error {
	a.sessionMu.Lock()
	if a.sessionID != "" {
		a.sessionMu.Unlock()
		return nil
	}
	a.sessionMu.Unlock()

	id := a.nextRPCID()
	initBody := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "social-ui", "version": Version},
		},
	}
	_, sid, err := a.doMCP(ctx, initBody)
	if err != nil {
		return err
	}
	if sid == "" {
		return fmt.Errorf("mcp initialize: server did not return Mcp-Session-Id")
	}
	a.sessionMu.Lock()
	a.sessionID = sid
	a.sessionMu.Unlock()

	// Send notifications/initialized so the server knows we're ready.
	notif := map[string]any{
		"jsonrpc": "2.0",
		"method":  "notifications/initialized",
	}
	_, _, _ = a.doMCP(ctx, notif)
	return nil
}

// doMCP issues one JSON-RPC body to the host MCP and returns the
// response body + any new Mcp-Session-Id header (only set during
// initialize). Bearer header included when the operator set
// MCP_AUTH_TOKEN.
func (a *api) doMCP(ctx context.Context, body map[string]any) ([]byte, string, error) {
	buf, err := json.Marshal(body)
	if err != nil {
		return nil, "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.agentURL, bytes.NewReader(buf))
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if a.bearer != "" {
		req.Header.Set("Authorization", "Bearer "+a.bearer)
	}
	a.sessionMu.Lock()
	if a.sessionID != "" {
		req.Header.Set("Mcp-Session-Id", a.sessionID)
	}
	a.sessionMu.Unlock()

	resp, err := a.httpc.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", err
	}
	if resp.StatusCode == http.StatusAccepted {
		// 202 = no result expected (notifications/initialized).
		return respBytes, "", nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("mcp http %d: %s", resp.StatusCode, snippet(respBytes))
	}
	newSID := resp.Header.Get("Mcp-Session-Id")
	return respBytes, newSID, nil
}

func (a *api) nextRPCID() int {
	a.sessionMu.Lock()
	a.rpcID++
	v := a.rpcID
	a.sessionMu.Unlock()
	return v
}

// ---- helpers ----------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeAPIError(w http.ResponseWriter, err error) {
	writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 240 {
		s = s[:237] + "..."
	}
	return s
}
