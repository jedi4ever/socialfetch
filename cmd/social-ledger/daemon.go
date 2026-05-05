package main

// CLI dispatcher for the ledger HTTP daemon. Mirrors the
// social-fetch headless / bridge subcommand layout (run / start /
// stop / status) so operators have one mental model for "social-
// skills' local services". The daemon itself lives in
// internal/ledger/daemon.go; this file only handles process
// lifecycle (PID file, fork-detached spawn, graceful stop).

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jedi4ever/social-skills/internal/ledger"
)

// cmdDaemon dispatches the daemon subcommands.
func cmdDaemon(args []string) error {
	sub := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub = args[0]
		args = args[1:]
	}
	switch sub {
	case "", "run":
		return runDaemonForeground(args)
	case "start":
		return runDaemonStart(args)
	case "stop":
		return runDaemonStop(args)
	case "status", "ping":
		return runDaemonStatus(args)
	case "monitor", "watch":
		return runDaemonMonitor(args)
	}
	if sub == "-h" || sub == "--help" {
		printDaemonHelp()
		return nil
	}
	return fmt.Errorf("daemon: unknown subcommand %q (try `daemon --help`)", sub)
}

func printDaemonHelp() {
	fmt.Print(`social-ledger daemon — long-lived HTTP wrapper around the SQLite ledger

Usage:
  social-ledger daemon [run]         run in foreground (default)
  social-ledger daemon start         fork a detached daemon (writes PID file)
  social-ledger daemon stop          stop the running daemon
  social-ledger daemon status        report daemon health
  social-ledger daemon monitor       live-tail recent events (refresh 1s)

Common flags:
  --port N                           loopback port (default 5557) — shortcut
                                     for --bind 127.0.0.1:N
  --bind ADDR                        full listen address; use 0.0.0.0:N to
                                     expose on all interfaces (no auth — LAN
                                     or SSH-tunnel only)
  --data-dir DIR                     ledger directory (default $SOCIAL_LEDGER_DIR
                                     or $XDG_DATA_HOME/social-ledger)
  --json                             machine-readable output (status only)

Env vars:
  SOCIAL_LEDGER_DAEMON_URL           where clients look for the daemon
                                     (default http://127.0.0.1:5557)
  SOCIAL_LEDGER_DAEMON_DISABLE       non-empty = clients skip daemon, use
                                     direct store / subprocess
  SOCIAL_LEDGER_DIR                  ledger data directory

Endpoints (when running):
  POST http://ADDR/ingest      {"items":[...]} → {total,new,updated,unchanged}
  POST http://ADDR/forget      {"key|url":"..."} → {deleted}
  GET  http://ADDR/search?q=...&limit=N → JSON array
  GET  http://ADDR/get?url=... or ?key=... → JSON item
  GET  http://ADDR/content?url=... → text/markdown body
  GET  http://ADDR/list?source=&since=&limit= → JSON array
  GET  http://ADDR/seen?url=... → {seen,key,source,last_seen_at}
  GET  http://ADDR/stats → store.Stats JSON
  GET  http://ADDR/status → daemon health
  POST http://ADDR/shutdown → graceful stop

Exit codes (status):
  0   running
  2   not reachable
`)
}

func ledgerStateDir() string {
	if d, err := os.UserCacheDir(); err == nil {
		return filepath.Join(d, "social-ledger")
	}
	return filepath.Join(os.TempDir(), "social-ledger")
}

func ledgerPIDFile() string { return filepath.Join(ledgerStateDir(), "daemon.pid") }
func ledgerLogFile() string { return filepath.Join(ledgerStateDir(), "daemon.log") }

// daemonFlags is the shared flag-parsing for run / start.
type daemonFlags struct {
	bind    string
	dataDir string
}

func parseDaemonFlags(args []string) (daemonFlags, error) {
	f := daemonFlags{
		bind: fmt.Sprintf("127.0.0.1:%d", ledger.DefaultDaemonPort),
	}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-h", "--help":
			printDaemonHelp()
			os.Exit(0)
		case "--port":
			i++
			if i >= len(args) {
				return f, fmt.Errorf("--port needs a value")
			}
			n, err := strconv.Atoi(args[i])
			if err != nil || n <= 0 || n > 65535 {
				return f, fmt.Errorf("--port: invalid value %q", args[i])
			}
			f.bind = fmt.Sprintf("127.0.0.1:%d", n)
		case "--bind":
			i++
			if i >= len(args) {
				return f, fmt.Errorf("--bind needs a value")
			}
			f.bind = args[i]
		case "--data-dir":
			i++
			if i >= len(args) {
				return f, fmt.Errorf("--data-dir needs a value")
			}
			f.dataDir = args[i]
		default:
			return f, fmt.Errorf("daemon: unknown argument %q", args[i])
		}
	}
	return f, nil
}

func runDaemonForeground(args []string) error {
	flags, err := parseDaemonFlags(args)
	if err != nil {
		return err
	}
	dir, err := resolveDataDir(flags.dataDir)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	dbPath := filepath.Join(dir, "ledger.db")

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	d := &ledger.Daemon{
		DBPath: dbPath,
		Logf: func(format string, a ...any) {
			fmt.Fprintf(os.Stderr, "ledger-daemon: "+format+"\n", a...)
		},
	}
	return d.Run(ctx, flags.bind)
}

func runDaemonStart(args []string) error {
	flags, err := parseDaemonFlags(args)
	if err != nil {
		return err
	}

	if pid, ok := readLedgerPID(); ok && processAlive(pid) {
		return fmt.Errorf("ledger daemon already running (pid %d) — `social-ledger daemon stop` first", pid)
	}

	if err := os.MkdirAll(ledgerStateDir(), 0o755); err != nil {
		return err
	}
	logF, err := os.OpenFile(ledgerLogFile(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open log %s: %w", ledgerLogFile(), err)
	}

	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmdArgs := []string{"daemon", "run", "--bind", flags.bind}
	if flags.dataDir != "" {
		cmdArgs = append(cmdArgs, "--data-dir", flags.dataDir)
	}
	cmd := exec.Command(exe, cmdArgs...)
	cmd.Stdout = logF
	cmd.Stderr = logF
	cmd.Stdin = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	if err := cmd.Start(); err != nil {
		_ = logF.Close()
		return fmt.Errorf("spawn ledger daemon: %w", err)
	}
	pid := cmd.Process.Pid
	_ = cmd.Process.Release()
	_ = logF.Close()

	if err := os.WriteFile(ledgerPIDFile(), fmt.Appendf(nil, "%d\n", pid), 0o644); err != nil {
		return fmt.Errorf("write pid file: %w", err)
	}

	// Health check: poll /status. SQLite open is fast (<100ms);
	// give 5s for any disk-init churn before reporting failure.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !processAlive(pid) {
			_ = os.Remove(ledgerPIDFile())
			return fmt.Errorf("ledger daemon spawned (pid %d) but exited — see %s (DB may be locked or %q already in use)",
				pid, ledgerLogFile(), flags.bind)
		}
		if ledgerReachable(flags.bind) {
			fmt.Printf("ledger daemon started (pid %d, bind %s, log %s)\n",
				pid, flags.bind, ledgerLogFile())
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("ledger daemon spawned (pid %d) but didn't open %s in 5s — check %s",
		pid, flags.bind, ledgerLogFile())
}

func runDaemonStop(args []string) error {
	for i := 0; i < len(args); i++ {
		if args[i] == "-h" || args[i] == "--help" {
			printDaemonHelp()
			return nil
		}
		return fmt.Errorf("daemon stop: unknown argument %q", args[i])
	}
	pid, ok := readLedgerPID()
	if !ok {
		fmt.Println("ledger daemon: no PID file (already stopped)")
		return nil
	}
	if !processAlive(pid) {
		_ = os.Remove(ledgerPIDFile())
		fmt.Printf("ledger daemon: pid %d not running, cleared PID file\n", pid)
		return nil
	}
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		return fmt.Errorf("kill pid %d: %w", pid, err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !processAlive(pid) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	_ = os.Remove(ledgerPIDFile())
	fmt.Printf("ledger daemon stopped (pid %d)\n", pid)
	return nil
}

func runDaemonStatus(args []string) error {
	jsonOut := false
	overrideURL := ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-h", "--help":
			printDaemonHelp()
			return nil
		case "--json":
			jsonOut = true
		case "--port":
			i++
			if i >= len(args) {
				return fmt.Errorf("--port needs a value")
			}
			n, err := strconv.Atoi(args[i])
			if err != nil || n <= 0 || n > 65535 {
				return fmt.Errorf("--port: invalid value %q", args[i])
			}
			overrideURL = fmt.Sprintf("http://127.0.0.1:%d", n)
		case "--bind":
			i++
			if i >= len(args) {
				return fmt.Errorf("--bind needs a value")
			}
			overrideURL = "http://" + args[i]
		default:
			return fmt.Errorf("daemon status: unknown argument %q", args[i])
		}
	}
	c := ledger.NewDaemonClient()
	if overrideURL != "" {
		c.BaseURL = overrideURL
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	st, err := c.Status(ctx)
	if err != nil {
		if jsonOut {
			_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"reachable": false})
		} else {
			fmt.Println("not reachable")
		}
		os.Exit(2)
	}
	if jsonOut {
		_ = json.NewEncoder(os.Stdout).Encode(st)
		return nil
	}
	fmt.Printf("running — db=%s up=%ds ingests=%d queries=%d\n",
		st.DBPath, st.UpSeconds, st.Ingests, st.Queries)
	return nil
}

// runDaemonMonitor polls /status every refresh interval and re-
// renders a live status panel with ANSI cursor moves so the
// terminal shows a live updating view rather than scrolling
// output. Mirrors the headless monitor's UX.
func runDaemonMonitor(args []string) error {
	refresh := 1 * time.Second
	overrideURL := ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-h", "--help":
			fmt.Print(`social-ledger daemon monitor — live tail of ledger events

Usage:
  social-ledger daemon monitor [flags]

Flags:
  --refresh DUR    poll interval (default 1s, e.g. 500ms / 2s)
  --port N         daemon port shortcut
  --bind ADDR      daemon address (e.g. remote-host:5557)

Ctrl-C to exit.
`)
			return nil
		case "--refresh":
			i++
			if i >= len(args) {
				return fmt.Errorf("--refresh needs a value")
			}
			d, err := time.ParseDuration(args[i])
			if err != nil || d <= 0 {
				return fmt.Errorf("--refresh: invalid duration %q", args[i])
			}
			refresh = d
		case "--port":
			i++
			if i >= len(args) {
				return fmt.Errorf("--port needs a value")
			}
			n, err := strconv.Atoi(args[i])
			if err != nil || n <= 0 || n > 65535 {
				return fmt.Errorf("--port: invalid value %q", args[i])
			}
			overrideURL = fmt.Sprintf("http://127.0.0.1:%d", n)
		case "--bind":
			i++
			if i >= len(args) {
				return fmt.Errorf("--bind needs a value")
			}
			overrideURL = "http://" + args[i]
		default:
			return fmt.Errorf("daemon monitor: unknown argument %q", args[i])
		}
	}

	c := ledger.NewDaemonClient()
	if overrideURL != "" {
		c.BaseURL = overrideURL
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Hide cursor + restore on exit. Best-effort — terminals that
	// don't grok ANSI just see the literal escape (rare today).
	fmt.Print("\x1b[?25l")
	defer fmt.Print("\x1b[?25h\n")

	first := true
	for {
		probeCtx, cancelProbe := context.WithTimeout(ctx, 2*time.Second)
		st, err := c.Status(probeCtx)
		cancelProbe()

		if !first {
			fmt.Print("\x1b[H\x1b[J") // home + clear
		}
		first = false

		fmt.Printf("social-ledger daemon monitor — %s  (refresh %s)\n\n",
			time.Now().Format("15:04:05"), refresh)
		if err != nil {
			fmt.Printf("daemon not reachable: %v\n", err)
		} else {
			renderDaemonStatus(st)
		}

		select {
		case <-time.After(refresh):
		case <-ctx.Done():
			return nil
		}
	}
}

// renderDaemonStatus formats a status response as a human-readable
// panel — counters at the top, recent events below.
func renderDaemonStatus(st *ledger.StatusResponse) {
	fmt.Printf("running — db=%s up=%ds ingests=%d queries=%d\n",
		st.DBPath, st.UpSeconds, st.Ingests, st.Queries)

	if len(st.History) == 0 {
		fmt.Println("\n  (no events yet)")
		return
	}
	fmt.Println("\n  recent events:")
	max := 15
	if len(st.History) < max {
		max = len(st.History)
	}
	for i := 0; i < max; i++ {
		e := st.History[i]
		mark := "ok"
		if !e.OK {
			mark = "FAIL"
		}
		detail := e.Detail
		if len(detail) > 70 {
			detail = detail[:67] + "..."
		}
		fmt.Printf("    %s  %-6s  %-4s  %s\n",
			e.At.Format("15:04:05"), e.Kind, mark, detail)
	}
}

func readLedgerPID() (int, bool) {
	b, err := os.ReadFile(ledgerPIDFile())
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, true
}

// processAlive reports whether a PID is a running process.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}

// ledgerReachable hits /status on the daemon's bind address. For
// 0.0.0.0/:: binds we still poke 127.0.0.1 since loopback always
// reaches the daemon.
func ledgerReachable(bind string) bool {
	host := bind
	if i := strings.Index(host, ":"); i > 0 {
		hostOnly := host[:i]
		port := host[i:]
		if hostOnly == "" || hostOnly == "0.0.0.0" || hostOnly == "::" {
			host = "127.0.0.1" + port
		}
	}
	url := "http://" + host + "/status"
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == 200
}
