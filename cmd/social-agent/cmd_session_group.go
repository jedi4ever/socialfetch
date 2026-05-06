package main

// `social-agent session <verb>` — the noun-verb form of the
// session-lifecycle surface. Mirrors how `social-browser provider
// daytona <verb>` reads, and what most operators reach for when
// they think "I have a session, what can I do with it?".
//
//   social-agent session create [--workdir DIR] [--harness X] ...
//   social-agent session list
//   social-agent session resume <id>
//   social-agent session stop [<id>...]
//
// `create / list / stop` are aliases for the existing top-level
// shortcuts (`up / ls / down`) — same code path, different entry
// point. `resume` is genuinely new: drops the operator into the
// session's harness with conversation-history loaded
// (claude-code: `claude --continue`).

import (
	"flag"
	"fmt"
	"os"

	"github.com/jedi4ever/social-skills/internal/agent"
	"github.com/jedi4ever/social-skills/internal/agent/harness"
)

func cmdSession(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("session: <verb> required (try: create | list | resume | stop)")
	}
	switch args[0] {
	case "create":
		return cmdUp(args[1:])
	case "list":
		return cmdLs(args[1:])
	case "stop":
		return cmdDown(args[1:])
	case "resume":
		return cmdSessionResume(args[1:])
	default:
		return fmt.Errorf("session: unknown verb %q (try: create | list | resume | stop)", args[0])
	}
}

// cmdSessionResume drops into an existing session's harness with
// conversation-history loaded. Looks up the session's harness
// from List() output (Up stamps it as a label) so we know which
// CLI to start.
func cmdSessionResume(args []string) error {
	fs := flag.NewFlagSet("session resume", flag.ContinueOnError)
	provider := fs.String("provider", "docker", "substrate")
	tty := fs.Bool("tty", true, "allocate a TTY (default true)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		return fmt.Errorf("session resume: <id> required (see `social-agent ls`)")
	}
	id := fs.Arg(0)
	prov, err := buildProvider(*provider)
	if err != nil {
		return err
	}
	ctx, cancel := signalCtx()
	defer cancel()

	// Resolve session → harness. We need to know which harness
	// the container is running so we ask the right tool to
	// "resume". The provider's List output stamps it.
	sessions, err := prov.List(ctx)
	if err != nil {
		return err
	}
	matches := matchSessions(sessions, id)
	if len(matches) == 0 {
		return fmt.Errorf("no session matches %q (see `social-agent ls`)", id)
	}
	if len(matches) > 1 {
		return fmt.Errorf("multiple sessions match %q — use the full id", id)
	}
	hName := matches[0].Harness
	if hName == "" {
		hName = "claude-code"
	}
	h, err := harness.Get(hName)
	if err != nil {
		return err
	}
	return prov.Exec(ctx, matches[0].ID, agent.ExecOpts{
		Cmd:    h.ResumeCmd(),
		Stdin:  os.Stdin,
		Stdout: os.Stdout,
		Stderr: os.Stderr,
		TTY:    *tty,
	})
}
