package main

// CLI dispatcher for the headless-browser daemon. Mirrors the
// bridge subcommand layout (run / start / stop / status) so
// operators get one mental model for "social-fetch's local
// services". The daemon itself lives in
// internal/render/headless/daemon.go; this file only handles
// process lifecycle (PID file, fork-detached spawn, graceful stop).

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

	"github.com/jedi4ever/social-skills/internal/render/headless"
)

// runHeadless dispatches the headless subcommands.
func runHeadless(args []string) error {
	sub := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub = args[0]
		args = args[1:]
	}
	switch sub {
	case "", "run":
		return runHeadlessForeground(args)
	case "start":
		return runHeadlessStart(args)
	case "stop":
		return runHeadlessStop(args)
	case "status", "ping":
		return runHeadlessStatus(args)
	}
	if sub == "-h" || sub == "--help" {
		printHeadlessHelp()
		return nil
	}
	return fmt.Errorf("headless: unknown subcommand %q (try `headless --help`)", sub)
}

func printHeadlessHelp() {
	fmt.Print(`social-fetch headless — local pool of warm headless browsers

Usage:
  social-fetch headless [run]        run in foreground (default)
  social-fetch headless start        fork a detached daemon (writes PID file)
  social-fetch headless stop         stop the running daemon
  social-fetch headless status       report pool state

Common flags:
  --port N                           loopback port (default 5556) — shortcut
                                     for --bind 127.0.0.1:N
  --bind ADDR                        full listen address (default 127.0.0.1:5556)
                                     use 0.0.0.0:N to expose on all interfaces
  --pool N                           number of warm browsers (default 2)
  --recycle N                        kill+respawn each browser after N fetches
                                     (default 50; 0 = never)
  --json                             machine-readable output (status only)

Env vars:
  SOCIAL_FETCH_HEADLESS_POOL_SIZE      default for --pool
  SOCIAL_FETCH_HEADLESS_RECYCLE_AFTER  default for --recycle
  SOCIAL_FETCH_HEADLESS_DAEMON_URL     where clients look for the daemon
                                       (default http://127.0.0.1:5556)
  SOCIAL_FETCH_HEADLESS_DAEMON_DISABLE non-empty = clients never use the daemon

Endpoints (when running):
  POST http://ADDR/fetch     {"url": "..."} → {"html","final_url","engine"}
  GET  http://ADDR/status    pool state
  POST http://ADDR/shutdown  graceful stop

Exit codes (status):
  0   running
  2   not reachable
`)
}

func headlessStateDir() string {
	if d, err := os.UserCacheDir(); err == nil {
		return filepath.Join(d, "social-fetch")
	}
	return filepath.Join(os.TempDir(), "social-fetch")
}

func headlessPIDFile() string { return filepath.Join(headlessStateDir(), "headless.pid") }
func headlessLogFile() string { return filepath.Join(headlessStateDir(), "headless.log") }

// headlessFlags is the shared flag-parsing for `run` / `start` —
// extracted so both keep the same defaults + env overrides.
type headlessFlags struct {
	bind         string
	poolSize     int
	recycleAfter int
}

func parseHeadlessFlags(args []string) (headlessFlags, error) {
	f := headlessFlags{
		bind:         fmt.Sprintf("127.0.0.1:%d", headless.DefaultDaemonPort),
		poolSize:     headless.DefaultPoolSize,
		recycleAfter: headless.DefaultRecycleAfter,
	}
	if v := os.Getenv("SOCIAL_FETCH_HEADLESS_POOL_SIZE"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			f.poolSize = n
		}
	}
	if v := os.Getenv("SOCIAL_FETCH_HEADLESS_RECYCLE_AFTER"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			f.recycleAfter = n
		}
	}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-h", "--help":
			printHeadlessHelp()
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
		case "--pool":
			i++
			if i >= len(args) {
				return f, fmt.Errorf("--pool needs a value")
			}
			n, err := strconv.Atoi(args[i])
			if err != nil || n <= 0 {
				return f, fmt.Errorf("--pool: invalid value %q", args[i])
			}
			f.poolSize = n
		case "--recycle":
			i++
			if i >= len(args) {
				return f, fmt.Errorf("--recycle needs a value")
			}
			n, err := strconv.Atoi(args[i])
			if err != nil || n < 0 {
				return f, fmt.Errorf("--recycle: invalid value %q", args[i])
			}
			f.recycleAfter = n
		default:
			return f, fmt.Errorf("headless: unknown argument %q", args[i])
		}
	}
	return f, nil
}

func runHeadlessForeground(args []string) error {
	flags, err := parseHeadlessFlags(args)
	if err != nil {
		return err
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	d := &headless.Daemon{
		PoolSize:     flags.poolSize,
		RecycleAfter: flags.recycleAfter,
		Logf: func(format string, a ...any) {
			fmt.Fprintf(os.Stderr, "headless: "+format+"\n", a...)
		},
	}
	return d.Run(ctx, flags.bind)
}

func runHeadlessStart(args []string) error {
	flags, err := parseHeadlessFlags(args)
	if err != nil {
		return err
	}

	if pid, ok := readHeadlessPID(); ok && processAlive(pid) {
		return fmt.Errorf("headless already running (pid %d) — `social-fetch headless stop` first", pid)
	}

	if err := os.MkdirAll(headlessStateDir(), 0o755); err != nil {
		return err
	}
	logF, err := os.OpenFile(headlessLogFile(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open log %s: %w", headlessLogFile(), err)
	}

	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, "headless", "run",
		"--bind", flags.bind,
		"--pool", strconv.Itoa(flags.poolSize),
		"--recycle", strconv.Itoa(flags.recycleAfter),
	)
	cmd.Stdout = logF
	cmd.Stderr = logF
	cmd.Stdin = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	if err := cmd.Start(); err != nil {
		_ = logF.Close()
		return fmt.Errorf("spawn headless: %w", err)
	}
	pid := cmd.Process.Pid
	_ = cmd.Process.Release()
	_ = logF.Close()

	if err := os.WriteFile(headlessPIDFile(), fmt.Appendf(nil, "%d\n", pid), 0o644); err != nil {
		return fmt.Errorf("write pid file: %w", err)
	}

	// Health check: poll /status. The pool size = N browsers each
	// taking ~2s to launch — give 30s for cold start before
	// reporting failure. After warmup, the daemon answers /status
	// instantly.
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if !processAlive(pid) {
			_ = os.Remove(headlessPIDFile())
			return fmt.Errorf("headless spawned (pid %d) but exited — see %s (Chrome may be missing or %q already in use)",
				pid, headlessLogFile(), flags.bind)
		}
		if headlessReachable(flags.bind) {
			fmt.Printf("headless started (pid %d, bind %s, pool %d, recycle %d, log %s)\n",
				pid, flags.bind, flags.poolSize, flags.recycleAfter, headlessLogFile())
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("headless spawned (pid %d) but didn't open %s in 30s — check %s",
		pid, flags.bind, headlessLogFile())
}

func runHeadlessStop(args []string) error {
	for i := 0; i < len(args); i++ {
		if args[i] == "-h" || args[i] == "--help" {
			printHeadlessHelp()
			return nil
		}
		return fmt.Errorf("headless stop: unknown argument %q", args[i])
	}
	pid, ok := readHeadlessPID()
	if !ok {
		fmt.Println("headless: no PID file (already stopped)")
		return nil
	}
	if !processAlive(pid) {
		_ = os.Remove(headlessPIDFile())
		fmt.Printf("headless: pid %d not running, cleared PID file\n", pid)
		return nil
	}
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		return fmt.Errorf("kill pid %d: %w", pid, err)
	}
	// Wait briefly for the process to exit.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !processAlive(pid) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	_ = os.Remove(headlessPIDFile())
	fmt.Printf("headless stopped (pid %d)\n", pid)
	return nil
}

func runHeadlessStatus(args []string) error {
	jsonOut := false
	overrideURL := ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-h", "--help":
			printHeadlessHelp()
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
			return fmt.Errorf("headless status: unknown argument %q", args[i])
		}
	}
	c := headless.NewDaemonClient()
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
	fmt.Printf("running — pool=%d recycle_after=%d uses_remaining=%v\n",
		st.PoolSize, st.RecycleAfter, st.UsesRemaining)
	return nil
}

func readHeadlessPID() (int, bool) {
	b, err := os.ReadFile(headlessPIDFile())
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, true
}

func headlessReachable(bind string) bool {
	// bind looks like "127.0.0.1:5556" or "0.0.0.0:5556". Convert
	// to a probe URL — for 0.0.0.0 we still poke 127.0.0.1 since
	// the daemon listens on all interfaces and loopback always
	// reaches it.
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
