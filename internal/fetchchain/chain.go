// Package fetchchain orchestrates per-platform fetch fallback chains.
// Each fetcher (linkedin / medium / substack / article / twitter /
// arxiv / etc.) registers a fixed set of named methods (`bridge`,
// `http`, `jina`, `api`, `syndication`) along with a default order.
// Operators override the order via SOCIAL_FETCH_CHAIN_<PLATFORM>
// env var; the chain walks methods in order and returns the first
// successful result.
//
// The package is deliberately platform-agnostic — it knows nothing
// about HTTP, the bridge daemon, Jina, or any specific API. Each
// platform supplies a `runners` map keyed by Method name. The chain
// just orchestrates "try → on error → try next → log via audit".
//
// Why a tiny shared helper instead of platform-local fallback code:
// today's hand-rolled fallbacks are duplicated (Medium and Substack
// have identical bridgeOrDirect implementations) and divergent
// (LinkedIn has none, Article has the richest). A single primitive
// keeps the order definition declarative + lets operators reorder
// per-platform without code edits.
package fetchchain

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/jedi4ever/social-skills/internal/core"
)

// Method is the canonical name for a fetch path. Each platform
// declares which methods it supports; unknown / unsupported entries
// in an env var are silently skipped.
type Method string

const (
	MethodBridge      Method = "bridge"
	MethodHTTP        Method = "http"
	MethodJina        Method = "jina"
	MethodAPI         Method = "api"
	MethodSyndication Method = "syndication"
	// MethodHeadless drives a fresh headless browser locally
	// (chromedp under the hood today; engine-neutral name so a
	// future playwright-go alternative can drop in without
	// changing the chain method). Anonymous-but-local middle
	// ground between bridge (logged-in Chrome) and jina (remote
	// service).
	MethodHeadless Method = "headless"
)

// Runner is the per-method handler. Returns whatever the platform
// considers a "complete fetch result" — the chain doesn't inspect
// the value, only the error. nil error = success → chain stops.
// Non-nil error = chain logs and tries the next method.
//
// Generic over T so each platform can return its own native shape
// (most return *core.Item; the article fetcher's HTTP runner
// returns raw HTML for further extraction).
type Runner[T any] func(ctx context.Context, raw string) (T, error)

// ErrAllFailed is returned by Run when every method in the chain
// errored. The wrapped errs slice carries each failure in chain
// order so the caller can produce a useful aggregate error
// message.
var ErrAllFailed = errors.New("fetchchain: all methods failed")

// AllFailedError aggregates the per-method errors when ErrAllFailed
// surfaces. Callers can errors.Is(err, ErrAllFailed) to branch on
// the all-failed case AND errors.As to a *AllFailedError to inspect
// individual failures.
type AllFailedError struct {
	Errs []MethodError
}

// MethodError pairs a chain step with its error so the audit /
// final error message can name which method produced which
// failure. Important when an operator's chain is `bridge,http,jina`
// and they want to know whether the bridge timed out vs HTTP
// got CF-blocked vs Jina hit a rate limit.
type MethodError struct {
	Method Method
	Err    error
}

func (e *AllFailedError) Error() string {
	parts := make([]string, 0, len(e.Errs))
	for _, m := range e.Errs {
		parts = append(parts, fmt.Sprintf("%s: %v", m.Method, m.Err))
	}
	return fmt.Sprintf("%s [%s]", ErrAllFailed.Error(), strings.Join(parts, "; "))
}

func (e *AllFailedError) Is(target error) bool {
	return target == ErrAllFailed
}

// Resolve returns the chain to use. envValue is whatever the
// platform's SOCIAL_FETCH_CHAIN_<NAME> env var is set to; empty
// means "use default". defaultChain is the platform's hard-coded
// preferred order (preserves today's behaviour as the baseline).
//
// Methods listed in envValue that aren't in supported get filtered
// out — the resulting chain only contains methods the platform
// can actually run. If the filter leaves an empty chain the
// default wins, so a typo in the env var ("brige" instead of
// "bridge") doesn't accidentally disable the fetcher.
func Resolve(envValue string, defaultChain []Method, supported map[Method]bool) []Method {
	envValue = strings.TrimSpace(envValue)
	if envValue == "" {
		return defaultChain
	}
	var out []Method
	for _, raw := range strings.Split(envValue, ",") {
		m := Method(strings.ToLower(strings.TrimSpace(raw)))
		if m == "" {
			continue
		}
		if supported != nil && !supported[m] {
			continue
		}
		out = append(out, m)
	}
	if len(out) == 0 {
		return defaultChain
	}
	return out
}

// Run walks methods in order, calling the matching runner for each.
// First runner that returns a nil error wins — the chain stops and
// returns that result. On error, the audit logger gets a one-line
// message naming the method + reason, and the chain proceeds to
// the next method.
//
// Methods without a registered runner are skipped (audit-logged).
// Empty chain or all-skip returns ErrAllFailed with a zero-length
// AllFailedError.
//
// platform is the source name used in audit log lines (e.g.
// "linkedin"); only present so the audit output is greppable per
// platform.
func Run[T any](
	ctx context.Context,
	platform, raw string,
	audit *core.AuditLogger,
	chain []Method,
	runners map[Method]Runner[T],
) (T, Method, error) {
	var zero T
	if len(chain) == 0 {
		return zero, "", &AllFailedError{}
	}
	var errs []MethodError
	for _, m := range chain {
		runner, ok := runners[m]
		if !ok {
			if audit != nil {
				audit.Logf("%s: chain skip %s — no runner registered", platform, m)
			}
			errs = append(errs, MethodError{Method: m, Err: errors.New("no runner registered")})
			continue
		}
		if audit != nil {
			audit.Logf("%s: trying %s", platform, m)
		}
		result, err := runner(ctx, raw)
		if err == nil {
			return result, m, nil
		}
		if audit != nil {
			audit.Logf("%s: %s failed: %v", platform, m, err)
		}
		errs = append(errs, MethodError{Method: m, Err: err})
	}
	return zero, "", &AllFailedError{Errs: errs}
}

// FromEnv reads SOCIAL_FETCH_CHAIN_<PLATFORM> for the named
// platform. Convenience wrapper so callers don't reimplement the
// env-var name mangling. Returns the raw env value (caller passes
// it to Resolve along with the platform's default + supported set).
func FromEnv(platform string) string {
	envName := "SOCIAL_FETCH_CHAIN_" + strings.ToUpper(platform)
	return os.Getenv(envName)
}
