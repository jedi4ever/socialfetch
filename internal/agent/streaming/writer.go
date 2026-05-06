// Package streaming defines the JSONL event format for
// `social-agent run --stream`. One JSON object per line on
// stdout, each typed by a `kind` field. Operators (and parent
// agents) parse line-by-line as the run progresses.
//
// Events:
//
//	{"kind":"session","status":"up","id":"<container-id>"}
//	    Emitted at session creation. id is the substrate-scoped id.
//
//	{"kind":"text","content":"<line>"}
//	    Emitted for each line of claude's stdout. Today this is
//	    line-buffered (one event per terminal line); v0.16.4
//	    will plumb through claude-code's --output-format stream-json
//	    for true token-level deltas.
//
//	{"kind":"artifact","path":"<rel>","size":<bytes>,"sha256":"<hex>","mime":"<type>"}
//	    Emitted when a new file appears in /artifacts. Polled at 1s
//	    intervals during the run, plus a final scan after exec
//	    completes so files written in the last second don't get
//	    missed.
//
//	{"kind":"session","status":"down","id":"<container-id>"}
//	    Emitted before the container is removed.
//
//	{"kind":"done","exit_code":0}
//	    Final event. exit_code is non-zero when claude itself
//	    returned non-zero or when the docker exec failed.
//
//	{"kind":"error","error":"<msg>"}
//	    Out-of-band error during the run (e.g. the artifacts poll
//	    failed). Doesn't terminate the stream — `done` still comes.
package streaming

import (
	"encoding/json"
	"io"
	"sync"
)

// Event is the single typed envelope emitted on every line of
// the stream. Optional fields stay zero for kinds that don't use
// them; consumers JSON-decode and switch on `kind`.
type Event struct {
	Kind string `json:"kind"`

	// session
	ID     string `json:"id,omitempty"`
	Status string `json:"status,omitempty"` // "up" | "down"

	// text
	Content string `json:"content,omitempty"`

	// artifact
	Path   string `json:"path,omitempty"`
	Size   int64  `json:"size,omitempty"`
	SHA256 string `json:"sha256,omitempty"`
	Mime   string `json:"mime,omitempty"`

	// done
	ExitCode int `json:"exit_code,omitempty"`

	// error / done
	Error string `json:"error,omitempty"`

	// claude_event — raw JSONL line emitted by claude-code in
	// stream-json mode (kinds: assistant, user, system, result,
	// tool_use, tool_result, …). Body is the unparsed JSON so
	// downstream consumers can re-decode against claude-code's
	// own schema without us shadowing fields. We also surface
	// extracted text via Kind="text" + Content for backward
	// compat with consumers that only care about the assistant's
	// prose.
	Body json.RawMessage `json:"body,omitempty"`
}

// Writer is a goroutine-safe JSONL emitter. Wraps io.Writer with
// a mutex so the artifact poller and the stdout-line reader can
// emit concurrently without interleaving bytes mid-line.
type Writer struct {
	w  io.Writer
	mu sync.Mutex
}

// NewWriter returns a Writer that emits to w. Typical use:
// streaming.NewWriter(os.Stdout).
func NewWriter(w io.Writer) *Writer {
	return &Writer{w: w}
}

// Emit serialises e as JSON, appends a newline, writes
// atomically. Errors propagate to the caller; the docker
// provider logs and continues — losing one event is preferable
// to aborting the whole run.
func (sw *Writer) Emit(e Event) error {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	body, err := json.Marshal(e)
	if err != nil {
		return err
	}
	body = append(body, '\n')
	_, err = sw.w.Write(body)
	return err
}
