package mcp

// ledger_only_test.go pins the surface of NewLedgerOnlyServer.
// If a future refactor accidentally registers fetch/search/ask
// against the ledger-only flavor (or drops a ledger tool), this
// fails fast. Lets `social-ledger mcp` callers trust the catalog
// they advertise to operators.

import (
	"context"
	"strings"
	"testing"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
)

func TestNewLedgerOnlyServer_ToolsAreLedgerSurface(t *testing.T) {
	srv := NewLedgerOnlyServer(Config{Version: "test"})

	// Use the in-process session adapter to call tools/list.
	resp := srv.HandleMessage(context.Background(), []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	if resp == nil {
		t.Fatal("HandleMessage returned nil")
	}
	r, ok := resp.(mcpgo.JSONRPCResponse)
	if !ok {
		t.Fatalf("response type %T not JSONRPCResponse", resp)
	}
	listRes, ok := r.Result.(mcpgo.ListToolsResult)
	if !ok {
		t.Fatalf("result type %T not ListToolsResult", r.Result)
	}

	got := map[string]bool{}
	for _, tool := range listRes.Tools {
		got[tool.Name] = true
	}

	want := []string{
		"social_ledger_seen",
		"social_ledger_get",
		"social_ledger_list",
		"social_ledger_search",
		"social_ledger_stats",
		"social_ledger_record",
		"social_ledger_forget",
		"social_fetch_read_file",
	}
	for _, n := range want {
		if !got[n] {
			t.Errorf("ledger-only server missing tool %q", n)
		}
	}

	// Anything starting with social_fetch_ (other than read_file)
	// should NOT be here — would mean we accidentally registered
	// the social-fetch surface.
	for name := range got {
		if strings.HasPrefix(name, "social_fetch_") && name != "social_fetch_read_file" {
			t.Errorf("ledger-only server unexpectedly registered fetch tool %q", name)
		}
	}
}
