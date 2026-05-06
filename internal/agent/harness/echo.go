package harness

// echo.go is the minimal "is the harness layer plumbed correctly?"
// implementation. No external CLI required — just `echo` and
// `/bin/bash`, both present in any reasonable container base.
//
// Useful for:
//
//   - CI smoke tests (no ANTHROPIC_API_KEY needed)
//   - Diagnosing layer faults: if `social-agent run --harness echo
//     "hello"` doesn't echo "hello" back, the bug is in the docker
//     provider or the entrypoint, not in claude-code or the network.
//   - Local dev iteration on the social-agent CLI itself before
//     touching claude-code's behaviour.

// Echo is a stateless harness that replies with whatever it
// received. Registered alongside ClaudeCode; selectable via
// `social-agent run --harness echo "..."`.
type Echo struct{}

func (Echo) Name() string { return "echo" }

// InvokePrompt → `echo <prompt>`. The container's entrypoint exec's
// this directly, so the prompt comes back on stdout one shell-token
// at a time (echo joins argv with spaces). Verifies the
// host-to-container argv pipeline end-to-end.
func (Echo) InvokePrompt(prompt string) []string {
	return []string{"echo", prompt}
}

// InteractiveCmd drops into bash. Lets `social-agent exec <id>`
// against an echo-harness session reach a shell for poking around
// — handy when debugging the container's filesystem without
// loading the full claude-code stack.
func (Echo) InteractiveCmd() []string {
	return []string{"/bin/bash", "-l"}
}

// ResumeCmd has no real "resume" concept for the echo harness —
// there's no conversation history to continue. Falls back to the
// interactive bash shell so `social-agent session resume <id>`
// against an echo session at least does something useful.
func (Echo) ResumeCmd() []string {
	return Echo{}.InteractiveCmd()
}

// EnvFromHost: nothing to forward. The echo harness needs no auth.
func (Echo) EnvFromHost(host map[string]string) (map[string]string, error) {
	return map[string]string{}, nil
}

func init() {
	Register(Echo{})
}
