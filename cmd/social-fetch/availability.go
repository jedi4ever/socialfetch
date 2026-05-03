package main

// CLI-side wrapper around internal/availability — adds a one-shot
// bridge liveness probe (the bridge is dynamic, so the static
// "needs bridge" string from the shared catalog isn't enough for the
// list-table UX) and an ASCII status badge so columns stay aligned
// regardless of which terminal renders the output.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/jedi4ever/social-skills/internal/availability"
	"github.com/jedi4ever/social-skills/internal/bridge"
)

// providerStatus enriches the shared availability.Status with a live
// bridge probe so list output reads "needs bridge: connected" or
// "needs bridge: not running" instead of the static label.
func providerStatus(category, name string) string {
	s := availability.Status(category, name)
	if s == "needs bridge" {
		return s + ": " + bridgeStatusLabel()
	}
	return s
}

// bridgeStatusLabel probes the local bridge once per process via
// http.Get on /status with a tight timeout, then caches the result
// for the rest of the run. The list output is informational, not a
// real-time monitor — the cache keeps `social-fetch list` snappy
// even when the bridge port is firewalled and TCP would time out.
//
// Returns one of: "connected", "running (not connected)", "not running".
var (
	bridgeOnce  sync.Once
	bridgeLabel string
)

func bridgeStatusLabel() string {
	bridgeOnce.Do(func() {
		port := bridge.DefaultPort
		client := &http.Client{Timeout: 250 * time.Millisecond}
		resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/status", port))
		if err != nil {
			bridgeLabel = "not running"
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			bridgeLabel = "running (not connected)"
			return
		}
		var body struct {
			Connected bool `json:"connected"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&body)
		if body.Connected {
			bridgeLabel = "connected"
			return
		}
		bridgeLabel = "running (not connected)"
	})
	return bridgeLabel
}

// statusBadge wraps providerStatus into a fixed-width ASCII tag the
// list table uses to keep columns aligned regardless of which
// terminal renders the output. Avoids unicode so piping `list` into
// clipboard / log files stays portable.
func statusBadge(category, name string) string {
	switch s := providerStatus(category, name); {
	case s == "":
		return "[ok]    "
	case strings.HasPrefix(s, "missing"):
		return "[!auth] "
	case strings.HasPrefix(s, "needs bridge"):
		return "[bridge]"
	default:
		return "[??]    "
	}
}
