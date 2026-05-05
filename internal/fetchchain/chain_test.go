package fetchchain

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jedi4ever/social-skills/internal/core"
)

func TestResolve(t *testing.T) {
	supported := map[Method]bool{
		MethodBridge: true,
		MethodHTTP:   true,
		MethodJina:   true,
	}
	def := []Method{MethodHTTP, MethodBridge, MethodJina}

	cases := []struct {
		name string
		env  string
		want []Method
	}{
		{
			name: "empty env returns default",
			env:  "",
			want: def,
		},
		{
			name: "whitespace-only env returns default",
			env:  "   ",
			want: def,
		},
		{
			name: "single method",
			env:  "jina",
			want: []Method{MethodJina},
		},
		{
			name: "comma-separated",
			env:  "bridge,jina",
			want: []Method{MethodBridge, MethodJina},
		},
		{
			name: "case insensitive",
			env:  "BRIDGE, Jina",
			want: []Method{MethodBridge, MethodJina},
		},
		{
			name: "unsupported method dropped",
			env:  "frobnicator,jina",
			want: []Method{MethodJina},
		},
		{
			name: "all unsupported falls back to default",
			env:  "foo,bar,baz",
			want: def,
		},
		{
			name: "spaces between commas tolerated",
			env:  "bridge , http , jina",
			want: []Method{MethodBridge, MethodHTTP, MethodJina},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Resolve(c.env, def, supported)
			if !equalMethods(got, c.want) {
				t.Errorf("Resolve(%q) = %v, want %v", c.env, got, c.want)
			}
		})
	}
}

func TestRun_FirstSuccessWins(t *testing.T) {
	calls := []Method{}
	runners := map[Method]Runner[string]{
		MethodHTTP: func(_ context.Context, _ string) (string, error) {
			calls = append(calls, MethodHTTP)
			return "http-result", nil
		},
		MethodBridge: func(_ context.Context, _ string) (string, error) {
			calls = append(calls, MethodBridge)
			return "bridge-result", nil
		},
	}
	got, via, err := Run(context.Background(), "test", "url", nil,
		[]Method{MethodHTTP, MethodBridge}, runners)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got != "http-result" {
		t.Errorf("result = %q, want http-result", got)
	}
	if via != MethodHTTP {
		t.Errorf("via = %q, want http", via)
	}
	if len(calls) != 1 {
		t.Errorf("expected only 1 runner called, got %d (%v)", len(calls), calls)
	}
}

func TestRun_FallsThroughOnError(t *testing.T) {
	httpErr := errors.New("connection refused")
	runners := map[Method]Runner[string]{
		MethodHTTP: func(_ context.Context, _ string) (string, error) {
			return "", httpErr
		},
		MethodJina: func(_ context.Context, _ string) (string, error) {
			return "jina-result", nil
		},
	}
	got, via, err := Run(context.Background(), "test", "url", nil,
		[]Method{MethodHTTP, MethodJina}, runners)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got != "jina-result" {
		t.Errorf("result = %q, want jina-result", got)
	}
	if via != MethodJina {
		t.Errorf("via = %q, want jina", via)
	}
}

func TestRun_AllFailedAggregatesErrors(t *testing.T) {
	e1 := errors.New("bridge unreachable")
	e2 := errors.New("http blocked")
	e3 := errors.New("jina rate limited")
	runners := map[Method]Runner[string]{
		MethodBridge: func(_ context.Context, _ string) (string, error) { return "", e1 },
		MethodHTTP:   func(_ context.Context, _ string) (string, error) { return "", e2 },
		MethodJina:   func(_ context.Context, _ string) (string, error) { return "", e3 },
	}
	_, _, err := Run(context.Background(), "test", "url", nil,
		[]Method{MethodBridge, MethodHTTP, MethodJina}, runners)
	if err == nil {
		t.Fatal("expected error from all-failed chain")
	}
	if !errors.Is(err, ErrAllFailed) {
		t.Errorf("error should wrap ErrAllFailed, got %v", err)
	}
	var allFailed *AllFailedError
	if !errors.As(err, &allFailed) {
		t.Fatalf("error should unwrap to *AllFailedError, got %T", err)
	}
	if len(allFailed.Errs) != 3 {
		t.Errorf("expected 3 method errors, got %d", len(allFailed.Errs))
	}
	// Order matters — chain order should be preserved.
	wantOrder := []Method{MethodBridge, MethodHTTP, MethodJina}
	for i, want := range wantOrder {
		if allFailed.Errs[i].Method != want {
			t.Errorf("err[%d].Method = %q, want %q", i, allFailed.Errs[i].Method, want)
		}
	}
	// Aggregate message should contain each method name.
	msg := err.Error()
	for _, m := range wantOrder {
		if !strings.Contains(msg, string(m)) {
			t.Errorf("aggregate error missing method %q: %s", m, msg)
		}
	}
}

func TestRun_UnknownMethodSkipped(t *testing.T) {
	runners := map[Method]Runner[string]{
		MethodJina: func(_ context.Context, _ string) (string, error) {
			return "jina-result", nil
		},
	}
	// "frobnicator" has no runner — chain should skip it and try jina.
	got, via, err := Run(context.Background(), "test", "url", nil,
		[]Method{Method("frobnicator"), MethodJina}, runners)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got != "jina-result" {
		t.Errorf("result = %q, want jina-result", got)
	}
	if via != MethodJina {
		t.Errorf("via = %q, want jina", via)
	}
}

func TestRun_EmptyChain(t *testing.T) {
	_, _, err := Run(context.Background(), "test", "url", nil,
		[]Method{}, map[Method]Runner[string]{})
	if !errors.Is(err, ErrAllFailed) {
		t.Errorf("empty chain should return ErrAllFailed, got %v", err)
	}
}

func TestRun_AuditLogsAttempts(t *testing.T) {
	var lines []string
	audit := core.NewAuditLogger(&lineCapture{lines: &lines})

	runners := map[Method]Runner[string]{
		MethodHTTP: func(_ context.Context, _ string) (string, error) {
			return "", errors.New("got 403")
		},
		MethodJina: func(_ context.Context, _ string) (string, error) {
			return "ok", nil
		},
	}
	_, _, err := Run(context.Background(), "test", "https://x.example", audit,
		[]Method{MethodHTTP, MethodJina}, runners)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// Expect: trying http, http failed, trying jina (and a successful jina has no failure log).
	joined := strings.Join(lines, "\n")
	for _, want := range []string{
		"test: trying http",
		"test: http failed: got 403",
		"test: trying jina",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("audit missing %q\nfull:\n%s", want, joined)
		}
	}
}

func TestFromEnv(t *testing.T) {
	t.Setenv("SOCIAL_FETCH_CHAIN_LINKEDIN", "jina,bridge")
	if got := FromEnv("linkedin"); got != "jina,bridge" {
		t.Errorf("FromEnv(linkedin) = %q, want jina,bridge", got)
	}
	if got := FromEnv("LinkedIn"); got != "jina,bridge" {
		t.Errorf("case-insensitive lookup failed, got %q", got)
	}
	if got := FromEnv("nonexistent"); got != "" {
		t.Errorf("FromEnv(nonexistent) = %q, want empty", got)
	}
}

// equalMethods is a tiny slice-equality helper since the test cases
// can't use reflect.DeepEqual without importing it for one use.
func equalMethods(a, b []Method) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// lineCapture is an io.Writer that splits writes into one entry per
// audit line. core.NewAuditLogger writes one Log call → one line, so
// this is sufficient for assertion.
type lineCapture struct {
	lines *[]string
}

func (l *lineCapture) Write(p []byte) (int, error) {
	*l.lines = append(*l.lines, strings.TrimRight(string(p), "\n"))
	return len(p), nil
}
