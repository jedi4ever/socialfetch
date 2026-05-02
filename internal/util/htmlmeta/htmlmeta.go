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
	Doc          *html.Node        // parsed root, exposed for host-specific extractors
	Meta         map[string]string // both `name=` and `property=` keyed
	Title        string
	CanonicalURL string
	LDJSON       []map[string]any
	ArticleHTML  string // raw inner HTML of the best article container — generic selectors
}

// Parse reads an HTML document from r and extracts all metadata. It never
// fails — malformed HTML still yields a usable (possibly partial) Page.
func Parse(r io.Reader) (*Page, error) {
	doc, err := html.Parse(r)
	if err != nil {
		return nil, err
	}

	p := &Page{Doc: doc, Meta: map[string]string{}}
	walk(doc, p)
	p.ArticleHTML = pickArticleFrom(doc, articleSelectors)
	return p, nil
}

// PickArticleHTML returns the inner HTML of the first matching selector
// with non-trivial text content. Host-specific extractors call this with
// their own selector list (e.g. Substack's ".body.markup" first).
func PickArticleHTML(doc *html.Node, selectors []string) string {
	return pickArticleFrom(doc, selectors)
}

// SelectFirst walks doc depth-first and returns the first node matching
// sel. Returns nil when nothing matches. Useful for grabbing host-specific
// elements (subtitles, byline avatars, comment counters).
//
// Supported selectors: "tag", ".class", "#id", "tag.class",
// "[attr=val]", "[attr]". For tag-with-multiple-classes, write
// "tag.class1.class2" (matches when both classes are present).
func SelectFirst(doc *html.Node, sel string) *html.Node {
	return find(doc, sel)
}

// SelectInnerHTML returns the inner HTML of the first match for sel, or
// the empty string when nothing matches.
func SelectInnerHTML(doc *html.Node, sel string) string {
	n := find(doc, sel)
	if n == nil {
		return ""
	}
	return innerHTML(n)
}

// TextOf returns the concatenated visible text of n.
func TextOf(n *html.Node) string { return textOf(n) }

// Attr returns n's attribute value for key, or "" if absent.
func Attr(n *html.Node, key string) string { return getAttr(n, key) }

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

// pickArticleFrom returns the inner HTML of the first selector with a
// non-trivial amount of text. The 50-char threshold rejects empty
// containers and tiny "Read more" wrappers without rejecting genuinely
// short notes (Substack often has 2-paragraph posts). Falls back to
// <body> when nothing matches.
func pickArticleFrom(doc *html.Node, selectors []string) string {
	for _, sel := range selectors {
		if node := find(doc, sel); node != nil {
			h := innerHTML(node)
			if visibleLen(h) >= 50 {
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
// Supported selectors: "tag", ".class", "#id", "tag.class",
// "tag.class1.class2" (all classes must be present), "[role=main]",
// "[attr]" (just-presence).
func find(n *html.Node, sel string) *html.Node {
	tag, classes, id, attrKey, attrVal := parseSelector(sel)
	var visit func(*html.Node) *html.Node
	visit = func(n *html.Node) *html.Node {
		if n.Type == html.ElementNode && matchNode(n, tag, classes, id, attrKey, attrVal) {
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

// parseSelector returns the components of a CSS-ish selector. Multiple
// classes are supported via "tag.a.b" or ".a.b".
func parseSelector(sel string) (tag string, classes []string, id, attrKey, attrVal string) {
	if strings.HasPrefix(sel, "[") && strings.HasSuffix(sel, "]") {
		body := sel[1 : len(sel)-1]
		if i := strings.Index(body, "="); i >= 0 {
			return "", nil, "", body[:i], strings.Trim(body[i+1:], `"`)
		}
		return "", nil, "", body, ""
	}
	switch {
	case strings.HasPrefix(sel, "#"):
		id = sel[1:]
		return
	case strings.HasPrefix(sel, "."):
		classes = strings.Split(sel[1:], ".")
		return
	}
	// tag or tag.class.class
	if i := strings.Index(sel, "."); i >= 0 {
		tag = sel[:i]
		classes = strings.Split(sel[i+1:], ".")
	} else {
		tag = sel
	}
	return
}

func matchNode(n *html.Node, tag string, classes []string, id, attrKey, attrVal string) bool {
	if tag != "" && n.Data != tag {
		return false
	}
	if len(classes) > 0 {
		classAttr := getAttr(n, "class")
		for _, c := range classes {
			if !hasClass(classAttr, c) {
				return false
			}
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
