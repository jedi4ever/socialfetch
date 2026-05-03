package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jedi4ever/social-skills/internal/core"
)

// runMonitor tails the global audit JSONL file and pretty-prints events
// as they're appended. Use Ctrl-C to stop.
//
// Usage:
//
//	social-fetch monitor              tail the default audit file
//	social-fetch monitor --tail 0     start at end-of-file (default)
//	social-fetch monitor --tail 50    show the last 50 lines first
//	social-fetch monitor --since 1h   replay events from the last hour
//	social-fetch monitor --raw        emit raw JSONL (no colorization)
//	social-fetch monitor --filter X   only show lines whose msg contains X
//	social-fetch monitor --path PATH  override the audit file location
//
// The implementation polls os.Stat for size changes; this avoids a
// fsnotify dependency at the cost of ~250 ms latency on new events,
// which is well below human-perceivable for an interactive monitor.
func runMonitor(args []string) error {
	flags := struct {
		path   string
		tail   int
		since  time.Duration
		raw    bool
		filter string
	}{
		path: core.DefaultAuditPath(),
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch a {
		case "-h", "--help":
			printMonitorHelp(os.Stdout)
			return nil
		case "--path":
			i++
			if i >= len(args) {
				return fmt.Errorf("--path needs a value")
			}
			flags.path = args[i]
		case "--tail":
			i++
			if i >= len(args) {
				return fmt.Errorf("--tail needs a value")
			}
			n, err := atoi(args[i])
			if err != nil {
				return err
			}
			flags.tail = n
		case "--since":
			i++
			if i >= len(args) {
				return fmt.Errorf("--since needs a value")
			}
			d, err := time.ParseDuration(args[i])
			if err != nil {
				return fmt.Errorf("--since: %w", err)
			}
			flags.since = d
		case "--raw":
			flags.raw = true
		case "--filter":
			i++
			if i >= len(args) {
				return fmt.Errorf("--filter needs a value")
			}
			flags.filter = args[i]
		default:
			return fmt.Errorf("monitor: unknown argument %q", a)
		}
	}

	f, err := os.Open(flags.path)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "monitor: %s does not exist yet — waiting for first event\n", flags.path)
			f, err = waitForFile(flags.path)
		}
		if err != nil {
			return err
		}
	}
	defer f.Close()

	out := bufio.NewWriter(os.Stdout)
	defer out.Flush()
	render := func(line string) {
		if flags.filter != "" && !strings.Contains(line, flags.filter) {
			return
		}
		if flags.raw {
			fmt.Fprintln(out, line)
		} else {
			fmt.Fprintln(out, formatAuditLine(line))
		}
		_ = out.Flush()
	}

	// Replay tail / since first.
	if flags.tail > 0 || flags.since > 0 {
		replayHistory(f, flags.tail, flags.since, render)
	} else {
		// Default: start at end of file (live tail only).
		_, _ = f.Seek(0, io.SeekEnd)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	return tailFollow(ctx, f, flags.path, render)
}

// tailFollow reads `f` line-by-line, sleeping on EOF and resuming
// when the file grows. It also watches for file rotation: when the
// inode of `path` changes (the live file was renamed and a fresh one
// took its place — typically by the rotation logic in
// core.OpenGlobalAudit), the open handle is closed and the new file
// is followed instead.
//
// Implementation notes that took a debugging pass to discover:
//
//   - bufio.Scanner gives up permanently once it sees io.EOF, so a
//     naive Scan-loop only ever shows lines that existed at start.
//     We use bufio.Reader.ReadString('\n') instead — after EOF, a
//     subsequent ReadString call hits the underlying *os.File.Read
//     again and picks up bytes appended in the meantime.
//
//   - Partial lines (a write that hasn't reached '\n' yet) leave
//     bytes buffered inside the reader; we keep the same Reader
//     across iterations so the next chunk completes them.
//
//   - On rotation, the open fd still points to the renamed file (now
//     `.jsonl.1`) which no producer writes to. We periodically stat
//     `path` and reopen if the inode changed.
func tailFollow(ctx context.Context, f *os.File, path string, render func(string)) error {
	const pollEvery = 250 * time.Millisecond
	const rotateCheckEvery = 4 // every (rotateCheckEvery * pollEvery) = 1s

	rd := bufio.NewReaderSize(f, 64*1024)
	rotateTicks := 0
	for {
		// Drain whatever's currently readable. ReadString returns
		// (partial+err, io.EOF) on the last partial chunk; treat that
		// as "buffer not yet complete, come back later."
		for {
			line, err := rd.ReadString('\n')
			if line != "" && (err == nil || err == io.EOF) {
				if err == io.EOF {
					// No newline yet — re-buffer by un-reading bytes
					// would require a different reader; for our JSONL
					// case writes are always newline-terminated, so
					// just stop here and let the next iteration
					// continue. (Worst case: a torn write is dropped
					// once. JSONL writers never produce torn lines
					// since each line is one Write call.)
					break
				}
				render(strings.TrimRight(line, "\r\n"))
				continue
			}
			if err != nil && err != io.EOF {
				return err
			}
			break
		}

		// Rotation check: if the file at `path` is now a different
		// inode than the one we have open, reopen.
		rotateTicks++
		if rotateTicks >= rotateCheckEvery {
			rotateTicks = 0
			if newF, switched, err := maybeReopen(f, path); err == nil && switched {
				_ = f.Close()
				f = newF
				rd = bufio.NewReaderSize(f, 64*1024)
				continue
			}
		}

		select {
		case <-ctx.Done():
			return nil
		case <-time.After(pollEvery):
		}
	}
}

// maybeReopen returns a fresh handle on `path` when its inode has
// changed (rotation happened) or `path` no longer exists but reappears
// later. (true, nil) means caller should swap. (false, nil) means
// nothing to do.
func maybeReopen(current *os.File, path string) (*os.File, bool, error) {
	currentInfo, err := current.Stat()
	if err != nil {
		return nil, false, err
	}
	pathInfo, err := os.Stat(path)
	if err != nil {
		// File missing temporarily — keep tailing the open handle.
		return nil, false, nil
	}
	if os.SameFile(currentInfo, pathInfo) {
		return nil, false, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	return f, true, nil
}

// replayHistory streams existing lines from the start of the file
// honoring whichever of tail or since the user supplied. After the
// replay, the file offset is left at end-of-file ready for live
// follow.
func replayHistory(f *os.File, tail int, since time.Duration, render func(string)) {
	_, _ = f.Seek(0, io.SeekStart)
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	var buf []string
	for scanner.Scan() {
		line := scanner.Text()
		if since > 0 {
			if !lineWithinSince(line, since) {
				continue
			}
		}
		buf = append(buf, line)
		if tail > 0 && len(buf) > tail {
			buf = buf[len(buf)-tail:]
		}
	}
	for _, l := range buf {
		render(l)
	}
}

// waitForFile polls until path appears, then opens it. Used when the
// user starts monitor before any social-fetch invocation has run yet.
func waitForFile(path string) (*os.File, error) {
	for {
		f, err := os.Open(path)
		if err == nil {
			return f, nil
		}
		if !os.IsNotExist(err) {
			return nil, err
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// auditEvent is the JSONL record OpenGlobalAudit emits.
type auditEvent struct {
	Ts  string `json:"ts"`
	Pid int    `json:"pid"`
	Cmd string `json:"cmd"`
	Msg string `json:"msg"`
}

func parseAuditLine(line string) (auditEvent, bool) {
	var e auditEvent
	if err := json.Unmarshal([]byte(line), &e); err != nil {
		return auditEvent{}, false
	}
	return e, true
}

func lineWithinSince(line string, since time.Duration) bool {
	e, ok := parseAuditLine(line)
	if !ok {
		return true // can't parse — keep
	}
	t, err := time.Parse(time.RFC3339Nano, e.Ts)
	if err != nil {
		return true
	}
	return time.Since(t) <= since
}

// formatAuditLine renders one JSONL event as a human-friendly,
// optionally-colorized line. Colors only kick in when stdout is a tty
// (otherwise pipelines through grep / less stay clean).
func formatAuditLine(line string) string {
	e, ok := parseAuditLine(line)
	if !ok {
		// Not JSON — emit verbatim so legacy free-text lines still show.
		return line
	}
	t, err := time.Parse(time.RFC3339Nano, e.Ts)
	tsStr := e.Ts
	if err == nil {
		tsStr = t.Local().Format("15:04:05.000")
	}

	color := stdoutIsTTY()
	dim := func(s string) string {
		if !color {
			return s
		}
		return "\033[2m" + s + "\033[0m"
	}
	cyan := func(s string) string {
		if !color {
			return s
		}
		return "\033[36m" + s + "\033[0m"
	}
	red := func(s string) string {
		if !color {
			return s
		}
		return "\033[31m" + s + "\033[0m"
	}
	yellow := func(s string) string {
		if !color {
			return s
		}
		return "\033[33m" + s + "\033[0m"
	}

	msg := e.Msg
	low := strings.ToLower(msg)
	switch {
	case strings.Contains(low, "failed") || strings.Contains(low, "error"):
		msg = red(msg)
	case strings.Contains(low, "skip") || strings.Contains(low, "cap hit") || strings.Contains(low, "warn"):
		msg = yellow(msg)
	}

	return fmt.Sprintf("%s %s %s %s",
		dim(tsStr),
		dim(fmt.Sprintf("[%d]", e.Pid)),
		cyan(e.Cmd),
		msg,
	)
}

// stdoutIsTTY reports whether stdout is connected to a terminal so
// formatters can decide whether ANSI escapes are appropriate.
func stdoutIsTTY() bool {
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}

func printMonitorHelp(w io.Writer) {
	fmt.Fprintf(w, `social-fetch monitor — live tail of the global audit log

Usage:
  social-fetch monitor [flags]

The audit log lives at %s by default. Every fetch / search / timeline /
ask invocation appends events to it; this command tails them as they
arrive. Use Ctrl-C to stop.

Flags:
  --path PATH        override the audit file location
                     (or set SOCIALFETCH_AUDIT_PATH)
  --tail N           print the last N lines on start (default: 0)
  --since DURATION   replay events from the last DURATION (e.g. 5m, 1h)
  --filter STRING    only show lines whose message contains STRING
  --raw              emit raw JSONL (no colorization, no formatting)
  -h, --help         show this help

Disable the global audit entirely by setting SOCIALFETCH_AUDIT=0 in
the producing shell — monitor will then have nothing to follow.

Examples:
  social-fetch monitor                 # live tail, color
  social-fetch monitor --tail 50       # show recent then follow
  social-fetch monitor --since 30m     # replay last 30 minutes
  social-fetch monitor --filter linkedin
  social-fetch monitor --raw | jq      # pipe to jq for scripting
`, core.DefaultAuditPath())
}
