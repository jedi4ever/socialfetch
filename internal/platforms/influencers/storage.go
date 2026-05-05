package influencers

// Daemon-aware storage helpers used by Add / Remove / List / Get.
// Mirror the daemon-vs-subprocess pattern in `internal/ledger`,
// kept here rather than in the ledger package because they're
// subscription-specific (the listFromLedger filter is "rows
// where source=subscription"; the subprocess fallback for List
// would otherwise need to be re-implemented at every caller).

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/jedi4ever/social-skills/internal/core"
	"github.com/jedi4ever/social-skills/internal/ledger"
	"github.com/jedi4ever/social-skills/internal/ledger/store"
)

// listFromLedger pulls all subscription rows. Daemon path uses
// the daemon's /list with source filter; subprocess path runs
// `social-ledger list --source subscription -n N --format json`.
func listFromLedger(ctx context.Context, limit int) ([]core.Item, error) {
	if !ledger.Disabled() {
		c := ledger.NewDaemonClient()
		if c.Reachable(ctx) {
			items, err := c.List(ctx, store.ListOpts{
				Source: Source,
				Limit:  effectiveListLimit(limit),
			})
			if err != nil {
				return nil, err
			}
			out := make([]core.Item, 0, len(items))
			for _, it := range items {
				// Bridge item.Item → core.Item via JSON re-encode
				// (same trick as ledger.Get).
				raw, _ := json.Marshal(it)
				var ci core.Item
				if err := json.Unmarshal(raw, &ci); err == nil {
					out = append(out, ci)
				}
			}
			return out, nil
		}
	}
	return listViaSubprocess(ctx, limit)
}

// listViaSubprocess runs `social-ledger article list --source
// influencer --format json -n N`, parses the JSON output. Used
// when the daemon isn't reachable.
func listViaSubprocess(ctx context.Context, limit int) ([]core.Item, error) {
	bin, err := ledgerBinary()
	if err != nil {
		return nil, err
	}
	// SOCIAL_LEDGER_DIR propagates via env inheritance — don't
	// forward it as --data-dir, which would bypass the project
	// subdir resolution (see internal/ledger/ledger.go for the
	// same fix).
	args := []string{"article", "list", "--source", Source, "--format", "json", "-n", fmt.Sprintf("%d", effectiveListLimit(limit))}
	cmd := exec.CommandContext(ctx, bin, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("subscriptions: list subprocess: %s", strings.TrimSpace(stderr.String()))
	}
	var items []core.Item
	if err := json.Unmarshal(stdout.Bytes(), &items); err != nil {
		return nil, fmt.Errorf("subscriptions: parse list output: %w", err)
	}
	return items, nil
}

// removeViaSubprocess runs `social-ledger article forget <url>` so
// the subprocess fallback works without the daemon. Returns true
// when the row was actually deleted.
func removeViaSubprocess(ctx context.Context, urlStr string) (bool, error) {
	bin, err := ledgerBinary()
	if err != nil {
		return false, err
	}
	args := []string{"article", "forget", urlStr}
	cmd := exec.CommandContext(ctx, bin, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		// "not found" is the no-op case the daemon path also
		// returns false for; surface it the same way.
		if strings.Contains(stderr.String(), "not found") {
			return false, nil
		}
		return false, fmt.Errorf("subscriptions: forget subprocess: %s", strings.TrimSpace(stderr.String()))
	}
	// social-ledger forget prints "deleted: <key>" on success;
	// presence of "deleted" or non-empty stdout means it worked.
	return strings.Contains(stdout.String(), "deleted") || strings.TrimSpace(stdout.String()) != "", nil
}

// ledgerBinary mirrors ledger.binaryPath() (unexported in the
// ledger package). Resolves SOCIAL_LEDGER_BIN, then PATH lookup,
// then a sibling-of-our-binary guess. Errors when nothing works.
func ledgerBinary() (string, error) {
	if explicit := strings.TrimSpace(os.Getenv(ledger.BinaryEnv)); explicit != "" {
		if _, err := os.Stat(explicit); err == nil {
			return explicit, nil
		}
		return "", fmt.Errorf("%s=%q does not exist", ledger.BinaryEnv, explicit)
	}
	if p, err := exec.LookPath("social-ledger"); err == nil {
		return p, nil
	}
	if self, err := os.Executable(); err == nil {
		// social-fetch sits next to social-ledger after `make
		// build`. Same heuristic the ledger package uses.
		guess := strings.TrimSuffix(self, "/social-fetch") + "/social-ledger"
		if _, err := os.Stat(guess); err == nil {
			return guess, nil
		}
	}
	return "", fmt.Errorf("social-ledger not on $PATH (set %s or `go install ./cmd/social-ledger`)", ledger.BinaryEnv)
}

// effectiveListLimit picks how many rows to ask the underlying
// list call for. Caller-supplied 0 = no cap → ask for a large
// number (10k) since SQLite list is cheap and operators rarely
// have more subscriptions than that.
func effectiveListLimit(req int) int {
	if req > 0 {
		return req
	}
	return 10000
}

// silence unused-import lint if a refactor drops imports.
var _ = url.Parse
var _ = time.Now
