package browser

// Fleet is the in-memory state of a browser-pool daemon. Holds
// the backends provided by zero-or-more providers, picks one for
// each incoming request (least-loaded round-robin), and tracks
// per-backend health + in-flight counters.
//
// All public methods are safe for concurrent use by the
// daemon's HTTP handlers. The picker uses a single mutex; for
// the request volume the daemon sees (10s/sec at most) this is
// simpler than a lock-free round-robin and keeps the mental
// model trivial.

import (
	"errors"
	"sort"
	"sync"
	"sync/atomic"
)

// Fleet tracks a set of Backends + per-backend liveness +
// in-flight counters. Build via NewFleet; populate via Replace
// (typically when the daemon refreshes from the Provider).
type Fleet struct {
	mu       sync.Mutex
	backends []*fleetBackend
}

// fleetBackend wraps Backend with mutable runtime fields. The
// inFlight counter is atomic so request handlers can bump it
// without taking the fleet mutex; reads (for the picker) take
// the mutex anyway since the slice may be re-ordered.
type fleetBackend struct {
	Backend                // value-copied snapshot from Provider.List / Up
	inFlight  atomic.Int64 // incremented per-call, decremented after
	deadCount atomic.Int64 // consecutive failed health-checks; > 3 = quarantine
}

// NewFleet creates an empty Fleet. Use Replace to seed it from a
// provider listing.
func NewFleet() *Fleet {
	return &Fleet{}
}

// Replace swaps the entire backend list. Caller passes a fresh
// snapshot from Provider.List(); we wrap each in a fleetBackend
// and discard the old set. In-flight counters reset — anything
// in-flight at the moment of Replace is orphaned (the original
// goroutine still runs to completion, just no longer tracked).
//
// The simplest safe model for a tool that swaps fleets at human
// speed (operator runs `up` / `down`, then waits for the daemon
// to refresh).
func (f *Fleet) Replace(b []Backend) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*fleetBackend, len(b))
	for i, be := range b {
		out[i] = &fleetBackend{Backend: be}
	}
	f.backends = out
}

// All returns a snapshot copy of every Backend (for /status,
// /monitor, ls). Callers get a slice they can iterate without
// holding the mutex. In-flight + dead counters are read into the
// returned Backend.Labels under reserved keys ("inflight",
// "dead-count") so JSON consumers don't need a parallel call.
func (f *Fleet) All() []Backend {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Backend, len(f.backends))
	for i, fb := range f.backends {
		b := fb.Backend
		if b.Labels == nil {
			b.Labels = map[string]string{}
		} else {
			// Don't mutate the live map; copy.
			cp := make(map[string]string, len(b.Labels)+2)
			for k, v := range b.Labels {
				cp[k] = v
			}
			b.Labels = cp
		}
		b.Labels["__inflight"] = itoa(int(fb.inFlight.Load()))
		b.Labels["__dead_count"] = itoa(int(fb.deadCount.Load()))
		out[i] = b
	}
	return out
}

// Pick returns the backend with the lowest in-flight count among
// "ready" entries. Ties broken by ID lexicographically so the
// choice is deterministic.
//
// Returns ErrEmptyFleet when the fleet has zero ready backends.
// The daemon surfaces that as 503 to the client so the operator
// can `up` more capacity rather than hanging the request.
func (f *Fleet) Pick() (*Backend, ReleaseFunc, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	candidates := make([]*fleetBackend, 0, len(f.backends))
	for _, fb := range f.backends {
		if fb.State != "ready" {
			continue
		}
		candidates = append(candidates, fb)
	}
	if len(candidates) == 0 {
		return nil, noopRelease, ErrEmptyFleet
	}
	sort.Slice(candidates, func(i, j int) bool {
		ai := candidates[i].inFlight.Load()
		aj := candidates[j].inFlight.Load()
		if ai != aj {
			return ai < aj
		}
		return candidates[i].ID < candidates[j].ID
	})
	chosen := candidates[0]
	chosen.inFlight.Add(1)
	be := chosen.Backend // copy
	return &be, func() { chosen.inFlight.Add(-1) }, nil
}

// MarkDead bumps the dead-count for a backend by id. After 3
// consecutive failures, the backend's State flips to "dead" and
// the picker stops choosing it. The daemon's health-check loop
// MarkAlives surviving backends every cycle; alive resets the
// counter to 0.
func (f *Fleet) MarkDead(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, fb := range f.backends {
		if fb.ID == id {
			n := fb.deadCount.Add(1)
			if n >= 3 {
				fb.State = "dead"
			}
			return
		}
	}
}

// MarkAlive resets the dead counter and (re-)marks the backend
// "ready". Used by the health-check loop on every successful
// /status probe.
func (f *Fleet) MarkAlive(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, fb := range f.backends {
		if fb.ID == id {
			fb.deadCount.Store(0)
			fb.State = "ready"
			return
		}
	}
}

// UpdateBackend swaps the rotating fields (URL + Token) on an
// existing backend. Called after Provider.RefreshBackend returns
// a fresh value — Daytona signed URLs rotate URL + embedded auth
// together, so we replace both atomically rather than just the
// token. ID, Provider, and Labels stay pinned to the original.
func (f *Fleet) UpdateBackend(id string, b Backend) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, fb := range f.backends {
		if fb.ID == id {
			fb.URL = b.URL
			fb.Token = b.Token
			return
		}
	}
}

// ReleaseFunc decrements the in-flight counter for a chosen
// backend. The daemon calls it via defer right after Pick so
// every code path (success, error, panic) hits it.
type ReleaseFunc func()

func noopRelease() {}

// ErrEmptyFleet is returned by Pick when no backends are in the
// "ready" state. Surfaced to clients as 503.
var ErrEmptyFleet = errors.New("browser: no ready backends in fleet")

// itoa avoids importing strconv just for this; small + safe for
// the values we cram into label maps.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	pos := len(b)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		pos--
		b[pos] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		pos--
		b[pos] = '-'
	}
	return string(b[pos:])
}
