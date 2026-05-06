package harness

// claude_code_test.go locks in invariants of the inner-MCP wiring
// that the rest of the system relies on: both InvokePrompt and
// StreamJSONCmd must register the ask + social MCP servers, and
// the system prompt must keep mentioning the ledger policy so
// future edits don't silently strip it.

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestInnerMCPConfigJSON_RegistersBothServers(t *testing.T) {
	var cfg struct {
		MCPServers map[string]struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(innerMCPConfigJSON), &cfg); err != nil {
		t.Fatalf("innerMCPConfigJSON is not valid JSON: %v", err)
	}
	if len(cfg.MCPServers) != 2 {
		t.Fatalf("want 2 mcpServers (ask + social), got %d: %v", len(cfg.MCPServers), cfg.MCPServers)
	}
	ask, ok := cfg.MCPServers["ask"]
	if !ok {
		t.Fatal("missing mcpServers.ask")
	}
	if ask.Command != "/usr/local/bin/social-agent" {
		t.Errorf("ask.command = %q, want /usr/local/bin/social-agent", ask.Command)
	}
	if len(ask.Args) != 2 || ask.Args[0] != "ask-mcp" || ask.Args[1] != "serve" {
		t.Errorf("ask.args = %v, want [ask-mcp serve]", ask.Args)
	}
	social, ok := cfg.MCPServers["social"]
	if !ok {
		t.Fatal("missing mcpServers.social")
	}
	if social.Command != "/usr/local/bin/social-fetch" {
		t.Errorf("social.command = %q, want /usr/local/bin/social-fetch", social.Command)
	}
	if len(social.Args) != 1 || social.Args[0] != "mcp" {
		t.Errorf("social.args = %v, want [mcp]", social.Args)
	}
}

func TestInvokePrompt_IncludesMCPConfig(t *testing.T) {
	argv := ClaudeCode{}.InvokePrompt("hello")
	if !argvContainsFlag(argv, "--mcp-config", innerMCPConfigJSON) {
		t.Errorf("InvokePrompt argv missing --mcp-config <innerMCPConfigJSON>: %v", argv)
	}
	// The prompt must still be the final positional arg so claude
	// reads it as the user message.
	if argv[len(argv)-1] != "hello" {
		t.Errorf("InvokePrompt last arg = %q, want \"hello\"", argv[len(argv)-1])
	}
}

func TestStreamJSONCmd_IncludesMCPConfig(t *testing.T) {
	argv := ClaudeCode{}.StreamJSONCmd()
	if !argvContainsFlag(argv, "--mcp-config", innerMCPConfigJSON) {
		t.Errorf("StreamJSONCmd argv missing --mcp-config <innerMCPConfigJSON>: %v", argv)
	}
}

func TestArtifactsSystemPrompt_MentionsLedger(t *testing.T) {
	for _, want := range []string{
		"social_ledger_seen",
		"social_ledger_get",
		"social_fetch_fetch",
		"BEFORE fetching",
	} {
		if !strings.Contains(artifactsSystemPrompt, want) {
			t.Errorf("artifactsSystemPrompt missing %q — the ledger policy paragraph was dropped", want)
		}
	}
}

// argvContainsFlag reports whether argv contains `flag value` as
// adjacent elements. Mirrors how exec interprets `--foo bar`.
func argvContainsFlag(argv []string, flag, value string) bool {
	for i := 0; i < len(argv)-1; i++ {
		if argv[i] == flag && argv[i+1] == value {
			return true
		}
	}
	return false
}
