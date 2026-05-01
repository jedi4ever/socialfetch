package rss

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/patrickdebois/social-skills/internal/core"
)

const rssXML = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0" xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:content="http://purl.org/rss/1.0/modules/content/">
  <channel>
    <title>Example Feed</title>
    <link>https://example.com/</link>
    <description>An example.</description>
    <item>
      <title>First post</title>
      <link>https://example.com/1</link>
      <guid>https://example.com/1</guid>
      <pubDate>Wed, 01 May 2024 10:00:00 GMT</pubDate>
      <dc:creator>Alice</dc:creator>
      <description>summary</description>
      <content:encoded><![CDATA[<p>full content</p>]]></content:encoded>
      <category>tech</category>
      <category>go</category>
    </item>
    <item>
      <title>Second post</title>
      <link>https://example.com/2</link>
      <pubDate>Wed, 02 May 2024 10:00:00 GMT</pubDate>
      <description>another</description>
    </item>
  </channel>
</rss>`

const atomXML = `<?xml version="1.0" encoding="UTF-8"?>
<feed xmlns="http://www.w3.org/2005/Atom">
  <title>Atom Example</title>
  <link href="https://example.com/atom" rel="self"/>
  <link href="https://example.com/" rel="alternate"/>
  <entry>
    <title>Hello Atom</title>
    <id>tag:example,2024:1</id>
    <updated>2024-05-01T10:00:00Z</updated>
    <published>2024-05-01T09:00:00Z</published>
    <author><name>Bob</name></author>
    <link href="https://example.com/atom/1" rel="alternate"/>
    <summary>summary</summary>
    <content type="html">&lt;p&gt;hi&lt;/p&gt;</content>
    <category term="news"/>
  </entry>
</feed>`

func TestMatch(t *testing.T) {
	f := New()
	cases := []struct {
		raw  string
		want bool
	}{
		{"https://example.com/feed", true},
		{"https://example.com/index.xml", true},
		{"https://example.com/rss/", true},
		{"https://example.com/atom.xml", true},
		{"https://example.com/", false},
		{"https://example.com/post/123", false},
	}
	for _, c := range cases {
		u, _ := url.Parse(c.raw)
		if got := f.Match(u); got != c.want {
			t.Errorf("Match(%q) = %v, want %v", c.raw, got, c.want)
		}
	}
}

func TestParseRSS(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = w.Write([]byte(rssXML))
	}))
	defer srv.Close()

	item, err := New().Fetch(context.Background(), srv.URL+"/feed", core.DefaultOptions())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if item.Title != "Example Feed" {
		t.Errorf("title: %q", item.Title)
	}
	if got := len(item.Children); got != 2 {
		t.Fatalf("want 2 entries, got %d", got)
	}
	first := item.Children[0]
	if first.Title != "First post" || first.Author != "Alice" {
		t.Errorf("first: %+v", first)
	}
	if first.Content != "<p>full content</p>" {
		t.Errorf("content:encoded not preserved: %q", first.Content)
	}
	if len(first.Tags) != 2 || first.Tags[0] != "tech" {
		t.Errorf("tags: %v", first.Tags)
	}
	if first.Published == nil || first.Published.Day() != 1 {
		t.Errorf("published: %v", first.Published)
	}
}

func TestParseAtom(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/atom+xml")
		_, _ = w.Write([]byte(atomXML))
	}))
	defer srv.Close()

	item, err := New().Fetch(context.Background(), srv.URL+"/atom.xml", core.DefaultOptions())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if item.Title != "Atom Example" {
		t.Errorf("title: %q", item.Title)
	}
	if got := len(item.Children); got != 1 {
		t.Fatalf("want 1 entry, got %d", got)
	}
	c := item.Children[0]
	if c.Title != "Hello Atom" || c.Author != "Bob" {
		t.Errorf("entry: %+v", c)
	}
	if c.URL != "https://example.com/atom/1" {
		t.Errorf("link not picked: %q", c.URL)
	}
	if c.Published == nil || c.Published.Year() != 2024 {
		t.Errorf("published: %v", c.Published)
	}
}
