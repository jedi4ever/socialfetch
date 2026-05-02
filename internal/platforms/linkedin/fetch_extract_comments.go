package linkedin

import (
	"strings"

	"github.com/jedi4ever/socialfetch/internal/core"
	"github.com/jedi4ever/socialfetch/internal/util/htmlmd"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// extractComments walks the DOM for LinkedIn comment containers and
// returns a tree of core.Comment. Replies live inside the parent
// comment node; we recurse to populate Replies and bump Depth.
//
// LinkedIn class names drift over time, so we match on substring rather
// than exact equality. The selectors below cover post-page comment
// rails as of mid-2026; pulse articles have a slightly different
// container (`.comments-comments-list`) but the per-comment shape is
// the same.
//
// As a side effect, every node we treat as a comment is removed from
// the input tree — that prevents the post-body extractor from
// re-rendering the same content as flat text.
func extractComments(root *html.Node) []core.Comment {
	if root == nil {
		return nil
	}
	containers := findAll(root, classContainsAny(
		"comments-comments-list",
		"comments-comments-list__container",
	))
	if len(containers) == 0 {
		return nil
	}
	var out []core.Comment
	for _, c := range containers {
		out = append(out, walkComments(c, 0)...)
		// Detach the container so the body extractor won't pick it up.
		if c.Parent != nil {
			c.Parent.RemoveChild(c)
		}
	}
	return out
}

// walkComments returns the direct comments inside container, recursing
// to populate replies. Each LinkedIn comment node is identifiable by a
// class fragment like `comments-comment-entity` or
// `comments-comment-item`.
func walkComments(container *html.Node, depth int) []core.Comment {
	var out []core.Comment
	for _, n := range findAll(container, classContainsAny(
		"comments-comment-entity",
		"comments-comment-item",
	)) {
		// Skip nested comments — we'll pick those up via recursion from
		// the parent rather than as a top-level entry.
		if isNestedInComment(n, container) {
			continue
		}
		c := buildComment(n, depth)
		if c.Body == "" && len(c.Replies) == 0 {
			continue
		}
		out = append(out, c)
	}
	return out
}

func buildComment(n *html.Node, depth int) core.Comment {
	c := core.Comment{Depth: depth}

	// Author name: usually inside an actor/name element.
	if name := findFirst(n, classContainsAny(
		"comments-comment-meta__description-title",
		"comments-post-meta__name-text",
		"comments-comment-meta__actor-name",
		"comments-comment-meta__name",
		"actor-name",
	)); name != nil {
		c.Author = strings.TrimSpace(textOf(name))
	}

	// Author URL: nearest anchor pointing at /in/<user>/.
	if a := findFirst(n, isProfileAnchor); a != nil {
		c.ID = strings.TrimRight(getAttr(a, "href"), "/")
	}

	// Body: the comment text container. Convert to markdown using
	// htmlmd so links/code/blockquotes survive.
	if body := findFirst(n, classContainsAny(
		"comments-comment-item__main-content",
		"comments-comment-item-content-body",
		"feed-shared-text",
	)); body != nil {
		c.Body = strings.TrimSpace(htmlmd.Convert(renderHTML(body)))
		// Detach so the recursion below doesn't double-count its text
		// when picking up replies.
		body.Parent.RemoveChild(body)
	}

	// Replies: typically nested in a `.comments-comment-replies` block.
	if replies := findFirst(n, classContainsAny(
		"comments-comment-replies",
		"comments-comments-list--replies",
	)); replies != nil {
		c.Replies = walkComments(replies, depth+1)
	}
	return c
}

// isNestedInComment returns true if n sits inside another comment node
// that's a descendant of root. Lets us treat top-level comments and
// their replies separately.
func isNestedInComment(n, root *html.Node) bool {
	for p := n.Parent; p != nil && p != root; p = p.Parent {
		if p.Type != html.ElementNode {
			continue
		}
		cls := strings.ToLower(getAttr(p, "class"))
		if strings.Contains(cls, "comments-comment-entity") ||
			strings.Contains(cls, "comments-comment-item") {
			return true
		}
	}
	return false
}

// isProfileAnchor matches <a href="…/in/<user>/">. The fetcher uses
// this to capture commenter profile URLs.
func isProfileAnchor(n *html.Node) bool {
	if n.DataAtom != atom.A {
		return false
	}
	href := strings.ToLower(getAttr(n, "href"))
	return strings.Contains(href, "linkedin.com/in/") || strings.HasPrefix(href, "/in/")
}

// findAll is the multi-result counterpart of findFirst.
func findAll(n *html.Node, match func(*html.Node) bool) []*html.Node {
	if n == nil {
		return nil
	}
	var out []*html.Node
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n == nil {
			return
		}
		if n.Type == html.ElementNode && match(n) {
			out = append(out, n)
			// Don't descend further: nested matches inside a top-level
			// container will be picked up by walkComments separately.
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return out
}

// classContainsAny returns a matcher that succeeds when the element's
// class attribute contains any of the supplied substrings.
func classContainsAny(subs ...string) func(*html.Node) bool {
	return func(n *html.Node) bool {
		cls := strings.ToLower(getAttr(n, "class"))
		for _, s := range subs {
			if strings.Contains(cls, s) {
				return true
			}
		}
		return false
	}
}

// renderHTML serializes a single subtree back to HTML so we can hand it
// to htmlmd.Convert. The standard library's html.Render writes to an
// io.Writer; we wrap it in a strings.Builder.
func renderHTML(n *html.Node) string {
	var b strings.Builder
	if err := html.Render(&b, n); err != nil {
		return ""
	}
	return b.String()
}
