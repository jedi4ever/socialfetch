package main

// social-ledger watch — tail the ledger's audit log and
// pretty-print each subcommand invocation as it happens.
// Symmetric with `social-fetch monitor` for the parent binary;
// useful when running multiple agents in parallel and you want
// to see in real time which one's writing to the ledger.
//
//	social-ledger watch              tail audit.jsonl, follow forever
//	social-ledger watch --tail 50    show the last 50 lines first
//	social-ledger watch --since 1h   replay last hour, then follow
//	social-ledger watch --raw        emit raw JSONL (composes with jq)
//	social-ledger watch --path PATH  override audit file location
//	social-ledger watch --filter ingest  only events whose cmd contains "ingest"
//
// Implementation polls os.Stat for size changes — same trick the
// social-fetch monitor uses, no fsnotify dep, ~250ms latency on new
// events which is fine for human reading.

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

func cmdWatch(args []string) error {
	fs := flag.NewFlagSet("watch", flag.ContinueOnError)
	pathFlag := fs.String("path", "", "audit log file (default: $SOCIAL_LEDGER_AUDIT_PATH or platform-default)")
	tail := fs.Int("tail", 0, "show the last N lines before following (0 = start at end-of-file)")
	since := fs.Duration("since", 0, "replay events from the last DURATION before following (e.g. 1h, 30m)")
	raw := fs.Bool("raw", false, "emit raw JSONL instead of pretty-printing")
	filter := fs.String("filter", "", "only show events whose cmd or args contain this substring")
	if err := fs.Parse(args); err != nil {
		return err
	}

	path := *pathFlag
	if path == "" {
		path = auditPath()
	}
	if path == "" {
		return fmt.Errorf("watch: audit is disabled (SOCIAL_LEDGER_AUDIT=0). Re-enable or pass --path")
	}

	// Surface the path so the user knows where we're tailing —
	// helpful when --path was unspecified and it falls back to
	// the platform default.
	fmt.Fprintf(os.Stderr, "watching %s (Ctrl-C to stop)\n", path)

	// Open with retry: file may not exist yet on a fresh install.
	// Wait up to 5s for the audit logger to create it; if nothing
	// happens, error out so we don't poll forever on a typo.
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("watch: %s does not exist after 5s (no ledger activity yet?)", path)
		}
		time.Sleep(200 * time.Millisecond)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
		<-sig
		cancel()
	}()

	// Initial read: replay or skip-to-end based on --tail / --since.
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	var startOffset int64
	switch {
	case *tail > 0 || *since > 0:
		// Read all lines, filter to recent N or recent-since,
		// emit, then resume tailing from end-of-file.
		_, _ = f.Seek(0, io.SeekStart)
		all := readAuditLines(f)
		filtered := selectRecent(all, *tail, *since)
		for _, line := range filtered {
			emitAuditLine(line, *raw, *filter)
		}
		off, _ := f.Seek(0, io.SeekEnd)
		startOffset = off
	default:
		// Skip the existing content; only show new events.
		off, _ := f.Seek(0, io.SeekEnd)
		startOffset = off
	}

	// Poll loop: re-stat every 250ms, read any new bytes, emit.
	return tailFollow(ctx, path, f, startOffset, *raw, *filter)
}

// readAuditLines slurps every JSONL line from r. Bounded buffer
// (1 MiB) so a corrupt mega-line can't OOM the watcher.
func readAuditLines(r io.Reader) []string {
	out := []string{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

// selectRecent picks a slice of `lines` matching --tail (last N)
// or --since (within DURATION). When both are set, --since wins
// — it's the more selective filter.
func selectRecent(lines []string, tail int, since time.Duration) []string {
	if since > 0 {
		cutoff := time.Now().Add(-since)
		out := []string{}
		for _, l := range lines {
			var e auditEntry
			if err := json.Unmarshal([]byte(l), &e); err != nil {
				continue
			}
			t, err := time.Parse(time.RFC3339Nano, e.TS)
			if err != nil {
				continue
			}
			if t.After(cutoff) {
				out = append(out, l)
			}
		}
		return out
	}
	if tail > 0 && tail < len(lines) {
		return lines[len(lines)-tail:]
	}
	return lines
}

// tailFollow re-stats the file, reads any bytes appended past the
// last offset, splits on newlines, emits each. Polls every
// 250ms; same approach social-fetch monitor uses.
func tailFollow(ctx context.Context, path string, f *os.File, off int64, raw bool, filter string) error {
	leftover := ""
	for {
		select {
		case <-ctx.Done():
			fmt.Fprintln(os.Stderr, "\nstopped")
			return nil
		default:
		}
		st, err := os.Stat(path)
		if err != nil {
			time.Sleep(250 * time.Millisecond)
			continue
		}
		// Handle log rotation: if file shrank, reopen.
		if st.Size() < off {
			f.Close()
			fresh, err := os.Open(path)
			if err != nil {
				time.Sleep(250 * time.Millisecond)
				continue
			}
			f = fresh
			off = 0
			leftover = ""
		}
		if st.Size() == off {
			time.Sleep(250 * time.Millisecond)
			continue
		}
		if _, err := f.Seek(off, io.SeekStart); err != nil {
			return err
		}
		buf := make([]byte, st.Size()-off)
		n, err := f.Read(buf)
		if err != nil && err != io.EOF {
			return err
		}
		off += int64(n)
		chunk := leftover + string(buf[:n])
		// Split on \n, keep the partial trailing line for next iteration.
		parts := strings.Split(chunk, "\n")
		leftover = parts[len(parts)-1]
		for _, line := range parts[:len(parts)-1] {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			emitAuditLine(line, raw, filter)
		}
	}
}

// emitAuditLine pretty-prints one JSONL audit row, or passes it
// through unchanged when --raw is set. Filter check is
// case-insensitive substring match on cmd + args.
func emitAuditLine(line string, raw bool, filter string) {
	if raw {
		if filter == "" || strings.Contains(strings.ToLower(line), strings.ToLower(filter)) {
			fmt.Println(line)
		}
		return
	}
	var e auditEntry
	if err := json.Unmarshal([]byte(line), &e); err != nil {
		// Malformed line — pass through with a hint so the
		// operator notices.
		fmt.Println("# malformed:", line)
		return
	}
	if filter != "" {
		hay := strings.ToLower(e.Cmd + " " + e.Args)
		if !strings.Contains(hay, strings.ToLower(filter)) {
			return
		}
	}
	t, err := time.Parse(time.RFC3339Nano, e.TS)
	when := e.TS
	if err == nil {
		when = t.Local().Format("15:04:05")
	}
	status := "ok"
	if e.ExitCode != 0 {
		status = fmt.Sprintf("FAIL(%d)", e.ExitCode)
	}
	args := e.Args
	if len(args) > 80 {
		args = args[:77] + "..."
	}
	if e.Error != "" {
		fmt.Printf("%s  %-8s %-40s %s %dms — %s\n", when, e.Cmd, args, status, e.DurationMs, e.Error)
	} else {
		fmt.Printf("%s  %-8s %-40s %s %dms\n", when, e.Cmd, args, status, e.DurationMs)
	}
}
