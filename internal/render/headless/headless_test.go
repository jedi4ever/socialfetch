package headless

import (
	"strings"
	"testing"
	"time"
)

// TestOptionsFromEnv covers the env-var override layer. Defaults fall
// through when the var is unset; bad values fall through too rather
// than failing — same fail-soft policy as the Jina knobs.
func TestOptionsFromEnv(t *testing.T) {
	t.Run("all unset returns defaults", func(t *testing.T) {
		opts := OptionsFromEnv()
		if opts.UserAgent != DefaultOptions.UserAgent {
			t.Errorf("UserAgent = %q, want default", opts.UserAgent)
		}
		if opts.Headless != true {
			t.Errorf("Headless = %v, want true", opts.Headless)
		}
		if opts.Timeout != DefaultOptions.Timeout {
			t.Errorf("Timeout = %v, want default", opts.Timeout)
		}
	})

	t.Run("headless toggle off", func(t *testing.T) {
		t.Setenv("SOCIAL_FETCH_HEADLESS_HEADLESS", "false")
		if got := OptionsFromEnv().Headless; got != false {
			t.Errorf("Headless = %v, want false", got)
		}
	})

	t.Run("timeout parse", func(t *testing.T) {
		t.Setenv("SOCIAL_FETCH_HEADLESS_TIMEOUT", "120s")
		if got := OptionsFromEnv().Timeout; got != 120*time.Second {
			t.Errorf("Timeout = %v, want 120s", got)
		}
	})

	t.Run("timeout bad value falls back to default", func(t *testing.T) {
		t.Setenv("SOCIAL_FETCH_HEADLESS_TIMEOUT", "not-a-duration")
		if got := OptionsFromEnv().Timeout; got != DefaultOptions.Timeout {
			t.Errorf("Timeout = %v, want default %v", got, DefaultOptions.Timeout)
		}
	})

	t.Run("user agent override", func(t *testing.T) {
		t.Setenv("SOCIAL_FETCH_HEADLESS_USER_AGENT", "custom-ua/1.0")
		if got := OptionsFromEnv().UserAgent; got != "custom-ua/1.0" {
			t.Errorf("UserAgent = %q", got)
		}
	})

	t.Run("no auth cookie auto-injection", func(t *testing.T) {
		// LINKEDIN_LI_AT being set must NOT auto-inject — anonymous
		// is the default policy. Callers that want a cookie do it
		// programmatically via NewWithOptions(Options{Cookies:...}).
		t.Setenv("LINKEDIN_LI_AT", "AQEDxxx")
		opts := OptionsFromEnv()
		if len(opts.Cookies) != 0 {
			t.Errorf("expected no cookies from env, got %d", len(opts.Cookies))
		}
	})
}

// TestNewWithOptions_FillsDefaults — when a caller passes an
// Options struct with zero fields, NewWithOptions backfills them
// from DefaultOptions so partial overrides don't silently produce
// a misconfigured browser (e.g. zero timeout = no deadline).
func TestNewWithOptions_FillsDefaults(t *testing.T) {
	f := NewWithOptions(Options{Headless: true}) // explicit Headless, everything else zero
	if f.Options.UserAgent == "" {
		t.Error("UserAgent not filled")
	}
	if f.Options.Timeout == 0 {
		t.Error("Timeout not filled")
	}
	if f.Options.Locale == "" {
		t.Error("Locale not filled")
	}
	if f.Options.ViewportWidth == 0 || f.Options.ViewportHeight == 0 {
		t.Error("Viewport not filled")
	}
}

// TestRelevantCookies covers the cookie-domain filter that decides
// which cookies to inject for a given URL. Critical for the
// auto-injected LinkedIn LI_AT — it must NOT show up on a Medium
// fetch even if the env var is set globally.
func TestRelevantCookies(t *testing.T) {
	cookies := []Cookie{
		{Name: "li_at", Value: "x", Domain: ".linkedin.com"},
		{Name: "session", Value: "y", Domain: ".medium.com"},
		{Name: "no-domain", Value: "z"}, // no domain — must always skip
	}

	cases := []struct {
		url      string
		wantNum  int
		wantName string
	}{
		{"https://www.linkedin.com/posts/foo", 1, "li_at"},
		{"https://linkedin.com/in/jane", 1, "li_at"},
		{"https://medium.com/@x/post", 1, "session"},
		{"https://example.com/", 0, ""},
		{"https://www.LINKEDIN.COM/posts/foo", 1, "li_at"}, // case-insensitive host
	}
	for _, c := range cases {
		got := relevantCookies(c.url, cookies)
		if len(got) != c.wantNum {
			t.Errorf("url=%q got %d cookies, want %d", c.url, len(got), c.wantNum)
			continue
		}
		if c.wantNum > 0 && got[0].Name != c.wantName {
			t.Errorf("url=%q cookie name = %q, want %q", c.url, got[0].Name, c.wantName)
		}
	}
}

// TestHostOf — host extraction from messy URLs. Covers ports,
// query strings, fragments, mixed case, www stripping.
func TestHostOf(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"https://www.linkedin.com/posts/foo", "linkedin.com"},
		{"https://linkedin.com:443/in/jane", "linkedin.com"},
		{"https://Medium.COM/post?utm=x#frag", "medium.com"},
		{"http://example.com", "example.com"},
		{"", ""},
		{"not-a-url", "not-a-url"},
	}
	for _, c := range cases {
		if got := hostOf(c.in); got != c.want {
			t.Errorf("hostOf(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestStealthScript_HasNoSecrets — paranoid guard. The init script
// is a string constant; if anyone refactors it to read from an env
// var or hardcode credentials, this test catches the mistake before
// the value ships to a browser context.
func TestStealthScript_HasNoSecrets(t *testing.T) {
	for _, banned := range []string{"API_KEY", "SECRET", "TOKEN", "li_at"} {
		if strings.Contains(stealthScript, banned) {
			t.Errorf("stealthScript leaks %q — should be opaque JS only", banned)
		}
	}
}

// Compile-time guard: defaults stay sane after future refactors.
// Spotted by `go test` because the file initialises this var at
// package load.
var _ = func() bool {
	if DefaultOptions.UserAgent == "" || DefaultOptions.Timeout == 0 ||
		DefaultOptions.ViewportWidth == 0 || DefaultOptions.ViewportHeight == 0 {
		panic("DefaultOptions has a zero field — see headless.go")
	}
	return true
}()
