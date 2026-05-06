// Package browser holds the pluggable browser-pool layer that
// fronts a fleet of remote chromedp endpoints behind one local
// HTTP daemon. Clients (social-fetch, MCP server) point at
// `http://127.0.0.1:5560`; the daemon round-robins across the
// fleet and injects per-backend auth headers transparently.
//
// Responsibilities are split between two pieces:
//
//   - Provider — knows how to spin up / tear down / list backends
//     for a particular substrate (Daytona today; local-pool +
//     others later). Lives in internal/browser/providers/<name>.
//
//   - Daemon (this package's daemon.go) — substrate-agnostic HTTP
//     server that holds a Fleet of Backends and forwards /fetch
//     and /screenshot requests round-robin.
//
// The Backend struct (URL + Token + ID) is the contract between
// the two: providers produce Backends, the daemon consumes them.
package browser

import (
	"context"
	"time"
)

// Backend is one chromedp endpoint the daemon can forward to.
// Created by a Provider; consumed by the Fleet + daemon. Token
// is empty for unauthed local pools (e.g. a `social-fetch
// headless start` running on this host).
type Backend struct {
	// ID identifies the backend within its Provider — sandbox id
	// for Daytona, pid or socket path for a local pool. Globally
	// unique within the fleet.
	ID string

	// Provider names the substrate ("daytona" / "local" / ...).
	// The daemon uses this to dispatch RefreshToken back to the
	// right provider when a token expires.
	Provider string

	// URL is the chromedp endpoint base (e.g.
	// https://5556-<id>.daytonaproxy01.net or http://127.0.0.1:5556).
	// /fetch + /screenshot + /status hang off this.
	URL string

	// Token, when non-empty, attaches as both
	// Authorization: Bearer <token> and X-Daytona-Preview-Token:
	// <token> on every forwarded request. Daytona accepts either
	// header; sending both means the same code path serves a
	// generic bearer-auth proxy too.
	Token string

	// State tracks daemon-side health: "ready", "starting",
	// "dead". Set by the fleet's health-check loop.
	State string

	// Created is the wall-clock timestamp from the provider —
	// used purely for /monitor display ordering.
	Created time.Time

	// Labels carries provider-side metadata (region, instance
	// index, version that created it). Surfaced by
	// `social-browser provider <name> ls` for operator review.
	Labels map[string]string
}

// UpOpts is the patch a caller passes to Provider.Up. Each
// provider interprets only the fields that make sense for it
// (e.g. a future local provider ignores Region; Daytona uses it).
// New fields land here as new providers surface them; existing
// providers are free to ignore unknown values.
type UpOpts struct {
	// N is the number of backends to create.
	N int

	// Image / snapshot identifier. For Daytona this is the
	// snapshot name (e.g. "social-skills:0.13.15"); for a local
	// provider it's a no-op.
	Image string

	// Region or target zone (Daytona "us"/"eu"). Empty = provider
	// default.
	Region string

	// CPU / Memory / Disk sizing knobs. 0 = provider default.
	CPU    int
	Memory int
	Disk   int

	// Token is an optional pre-shared MCP_AUTH_TOKEN to bake
	// into each backend's environment. Empty = provider auto-
	// generates one.
	Token string

	// AutoStopMin: idle minutes before the provider auto-stops
	// (0 = never). Daytona-specific today; local providers ignore.
	AutoStopMin int

	// Labels added to every backend created by this call. The
	// daemon uses them to filter "ours" from the provider's
	// existing fleet.
	Labels map[string]string
}

// Provider is the substrate that creates and tears down browser
// backends. Implementations live in internal/browser/providers/<name>.
//
// The interface is deliberately small — the daemon doesn't know
// or care about provider-specific concepts like "snapshot" or
// "preview URL." Provider methods do the translation.
type Provider interface {
	// Name identifies the provider in CLI flags and labels
	// ("daytona" / "local" / ...). Stable across versions.
	Name() string

	// Up creates n new backends and returns them ready-to-serve.
	// The provider blocks until the underlying browser is
	// listening — caller can immediately forward /fetch.
	Up(ctx context.Context, opts UpOpts) ([]Backend, error)

	// Down removes backends. Empty ids = "every backend tagged
	// as mine" (provider-specific label filtering).
	Down(ctx context.Context, ids ...string) error

	// List returns every backend the provider currently knows
	// about. The daemon calls this on startup to rebuild fleet
	// state from the source of truth (no local persistence).
	// Filters to OUR-labelled backends only.
	List(ctx context.Context) ([]Backend, error)

	// RefreshBackend re-resolves URL + Token for an existing
	// backend (called when the daemon sees a 401). Returns the
	// updated Backend; the daemon swaps both fields in the fleet.
	//
	// URL refresh matters for providers using signed/rotating
	// preview URLs (Daytona — each call returns a fresh
	// short-token-embedded hostname). Providers that hold a stable
	// URL just return the existing one with whatever token they
	// produce.
	RefreshBackend(ctx context.Context, id string) (Backend, error)
}
