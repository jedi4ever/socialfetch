// Package htmlmeta extracts page metadata — meta tags, JSON-LD, canonical
// link, and the main article HTML — out of an HTML document. It uses
// golang.org/x/net/html for a real DOM parse, not regex hacks.
//
// Article fetchers (Medium, Substack, generic) share this layer so they
// can focus on host-specific quirks rather than reimplementing OG tag
// extraction.
package htmlmeta

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"

	"golang.org/x/net/html"
)

// Page holds everything extracted from one HTML document.
type Page struct {
	Meta         map[string]string // both `name=` and `property=` keyed
	Title        string
	CanonicalURL string
	LDJSON       []map[string]any
	ArticleHTML  string // raw inner HTML of the best article container
}

// Parse reads an HTML document from r and extracts all metadata. It never
// fails — malformed HTML still yields a usable (possibly partial) Page.
func Parse(r io.Reader) (*Page, error) {
	doc, err := html.Parse(r)
	if err != nil {
		return nil, err
	}

	p := &Page{Meta: map[string]string{}}
	walk(doc, p)
	p.ArticleHTML = pickArticle(doc)
	return p, nil
}

// articleSelectors lists CSS-ish selectors in priority order: more specific
// containers first, falling back to <body> if nothing else hits.
var articleSelectors = []string{
	"article",
	"main",
	"[role=main]",
	".post-content",
	".entry-content",
	".article-body",
	".article-content",
	"#content",
	".content",
}

func walk(n *html.Node, p *Page) {
	if n.Type == html.ElementNode {
		switch n.Data {
		case "title":
			if p.Title == "" {
				p.Title = strings.TrimSpace(textOf(n))
			}
		case "meta":
			name := getAttr(n, "name")
			prop := getAttr(n, "property")
			content := getAttr(n, "content")
			if content == "" {
				break
			}
			if prop != "" {
				p.Meta[strings.ToLower(prop)] = content
			}
			if name != "" {
				p.Meta[strings.ToLower(name)] = content
			}
		case "link":
			if strings.EqualFold(getAttr(n, "rel"), "canonical") {
				p.CanonicalURL = getAttr(n, "href")
			}
		case "script":
			if strings.EqualFold(getAttr(n, "type"), "application/ld+json") {
				p.LDJSON = append(p.LDJSON, parseLDJSON(textOf(n))...)
			}
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		walk(c, p)
	}
}

func parseLDJSON(s string) []map[string]any {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	// LD+JSON may be a single object or an array of objects.
	var asArray []map[string]any
	if err := json.Unmarshal([]byte(s), &asArray); err == nil {
		return asArray
	}
	var asObj map[string]any
	if err := json.Unmarshal([]byte(s), &asObj); err == nil {
		return []map[string]any{asObj}
	}
	return nil
}

// pickArticle returns the inner HTML of the first matching article-ish
// container with a meaningful amount of content.
func pickArticle(doc *html.Node) string {
	for _, sel := range articleSelectors {
		if node := find(doc, sel); node != nil {
			h := innerHTML(node)
			if visibleLen(h) > 200 {
				return h
			}
		}
	}
	if body := find(doc, "body"); body != nil {
		return innerHTML(body)
	}
	return ""
}

// find walks the tree depth-first and returns the first node matching sel.
// Supported selectors: "tag", ".class", "#id", "tag.class", "[role=main]".
// Multiple class names per element are honored.
func find(n *html.Node, sel string) *html.Node {
	tag, class, id, attrKey, attrVal := parseSelector(sel)
	var visit func(*html.Node) *html.Node
	visit = func(n *html.Node) *html.Node {
		if n.Type == html.ElementNode && matchNode(n, tag, class, id, attrKey, attrVal) {
			return n
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if r := visit(c); r != nil {
				return r
			}
		}
		return nil
	}
	return visit(n)
}

func parseSelector(sel string) (tag, class, id, attrKey, attrVal string) {
	if strings.HasPrefix(sel, "[") && strings.HasSuffix(sel, "]") {
		body := sel[1 : len(sel)-1]
		if i := strings.Index(body, "="); i >= 0 {
			return "", "", "", body[:i], strings.Trim(body[i+1:], `"`)
		}
		return "", "", "", body, ""
	}
	switch {
	case strings.HasPrefix(sel, "."):
		class = sel[1:]
	case strings.HasPrefix(sel, "#"):
		id = sel[1:]
	default:
		// tag or tag.class
		if i := strings.Index(sel, "."); i >= 0 {
			tag = sel[:i]
			class = sel[i+1:]
		} else {
			tag = sel
		}
	}
	return
}

func matchNode(n *html.Node, tag, class, id, attrKey, attrVal string) bool {
	if tag != "" && n.Data != tag {
		return false
	}
	if class != "" {
		if !hasClass(getAttr(n, "class"), class) {
			return false
		}
	}
	if id != "" && getAttr(n, "id") != id {
		return false
	}
	if attrKey != "" {
		v := getAttr(n, attrKey)
		if attrVal != "" && v != attrVal {
			return false
		}
		if v == "" {
			return false
		}
	}
	return true
}

func hasClass(classAttr, want string) bool {
	for _, c := range strings.Fields(classAttr) {
		if c == want {
			return true
		}
	}
	return false
}

func getAttr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if strings.EqualFold(a.Key, key) {
			return a.Val
		}
	}
	return ""
}

// textOf returns the concatenated text content of n.
func textOf(n *html.Node) string {
	var b strings.Builder
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

func innerHTML(n *html.Node) string {
	var b bytes.Buffer
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		_ = html.Render(&b, c)
	}
	return b.String()
}

// visibleLen approximates how much real text a chunk of HTML contains by
// stripping tags. We only need an order-of-magnitude check, so we avoid
// re-parsing.
func visibleLen(h string) int {
	n, depth := 0, 0
	for i := 0; i < len(h); i++ {
		c := h[i]
		switch {
		case c == '<':
			depth++
		case c == '>':
			if depth > 0 {
				depth--
			}
		case depth == 0:
			n++
		}
	}
	return n
}
