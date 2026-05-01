// Package duckduckgo implements a search.Provider backed by DDG's HTML
// "lite" endpoint, which works without auth or JS.
//
// The lite endpoint is a stable, JS-free version of the search results
// page intended for low-bandwidth clients. It is far easier to parse than
// the main duckduckgo.com page.
package duckduckgo

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/patrickdebois/social-skills/internal/core"
	"github.com/patrickdebois/social-skills/internal/search"

	"golang.org/x/net/html"
)

// Provider queries DDG and parses the HTML results.
type Provider struct {
	BaseURL string
}

func New() *Provider {
	return &Provider{BaseURL: "https://lite.duckduckgo.com/lite/"}
}

func (Provider) Name() string { return "duckduckgo" }

func (p *Provider) Search(ctx context.Context, query string, max int) ([]search.Result, error) {
	if max <= 0 {
		max = 10
	}
	u := p.BaseURL + "?" + url.Values{"q": {query}, "kl": {"us-en"}}.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(""))
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", core.UserAgent)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := core.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("ddg: HTTP %d", resp.StatusCode)
	}

	doc, err := html.Parse(resp.Body)
	if err != nil {
		return nil, err
	}
	return parseResults(doc, max), nil
}

// parseResults walks the DDG-lite document. Each hit is a <a class="result-link">
// followed (in adjacent table cells) by a <td class="result-snippet">. We
// don't depend on exact positions: we collect links and snippets in order
// and zip them up.
func parseResults(doc *html.Node, max int) []search.Result {
	var results []search.Result
	var pending search.Result

	var visit func(*html.Node)
	visit = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "a" && hasClass(n, "result-link") {
			pending.URL = href(n)
			pending.Title = strings.TrimSpace(textOf(n))
			pending.Source = "duckduckgo"
		}
		if n.Type == html.ElementNode && n.Data == "td" && hasClass(n, "result-snippet") {
			pending.Snippet = strings.TrimSpace(textOf(n))
			if pending.URL != "" {
				results = append(results, pending)
				pending = search.Result{}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if len(results) >= max {
				return
			}
			visit(c)
		}
	}
	visit(doc)

	// Some hits have no snippet — still emit them.
	if pending.URL != "" && len(results) < max {
		results = append(results, pending)
	}

	return results
}

func href(n *html.Node) string {
	for _, a := range n.Attr {
		if a.Key == "href" {
			// DDG lite sometimes wraps the destination in /l/?uddg=...
			// Unwrap so callers get the real target.
			if dest := unwrapDDGRedirect(a.Val); dest != "" {
				return dest
			}
			return a.Val
		}
	}
	return ""
}

func unwrapDDGRedirect(raw string) string {
	if !strings.Contains(raw, "uddg=") {
		return ""
	}
	// raw looks like //duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2F&rut=...
	if i := strings.Index(raw, "uddg="); i >= 0 {
		v, err := url.QueryUnescape(raw[i+len("uddg="):])
		if err != nil {
			return ""
		}
		// Strip any trailing ampersand-suffixed query params.
		if amp := strings.Index(v, "&"); amp >= 0 {
			v = v[:amp]
		}
		return v
	}
	return ""
}

func hasClass(n *html.Node, want string) bool {
	for _, a := range n.Attr {
		if a.Key == "class" {
			for _, c := range strings.Fields(a.Val) {
				if c == want {
					return true
				}
			}
		}
	}
	return false
}

func textOf(n *html.Node) string {
	var b bytes.Buffer
	var visit func(*html.Node)
	visit = func(n *html.Node) {
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			visit(c)
		}
	}
	visit(n)
	return b.String()
}
