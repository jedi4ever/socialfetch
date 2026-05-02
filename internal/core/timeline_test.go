package core

import "testing"

func TestParseIdentifier(t *testing.T) {
	cases := []struct {
		name         string
		input        string
		hint         string
		wantProvider string
		wantUser     string
		wantErr      bool
	}{
		{"bare handle defaults to x", "swyx", "", "x", "swyx", false},
		{"@ implies x", "@swyx", "", "x", "swyx", false},
		{"explicit linkedin hint", "patrickdebois", "linkedin", "linkedin", "patrickdebois", false},
		{"x url", "https://x.com/swyx", "", "x", "swyx", false},
		{"twitter url", "https://twitter.com/swyx", "", "x", "swyx", false},
		{"x url with status", "https://x.com/swyx/status/2050068468498842058", "", "x", "swyx", false},
		{"linkedin profile url", "https://www.linkedin.com/in/patrickdebois/", "", "linkedin", "patrickdebois", false},
		{"linkedin recent-activity url", "https://www.linkedin.com/in/patrickdebois/recent-activity/all/", "", "linkedin", "patrickdebois", false},
		{"empty input", "", "", "", "", true},
		{"unrecognised host", "https://facebook.com/foo", "", "", "", true},
		{"linkedin url without /in/", "https://www.linkedin.com/feed/", "", "", "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotProvider, gotUser, err := ParseIdentifier(c.input, c.hint)
			if c.wantErr {
				if err == nil {
					t.Fatalf("expected error, got (%q, %q)", gotProvider, gotUser)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if gotProvider != c.wantProvider {
				t.Errorf("provider = %q, want %q", gotProvider, c.wantProvider)
			}
			if gotUser != c.wantUser {
				t.Errorf("user = %q, want %q", gotUser, c.wantUser)
			}
		})
	}
}
