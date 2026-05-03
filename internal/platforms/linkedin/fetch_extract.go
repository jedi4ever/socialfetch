package linkedin

import (
	"bytes"
	"strings"

	"github.com/jedi4ever/social-skills/internal/core"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// cleanHTML strips LinkedIn's navigation chrome, sign-in CTAs, button
// rails, and other boilerplate from the rendered DOM, then returns
// both:
//
//   - htmlBody: the cleaned content fragment (HTML), ready to feed to
//     htmlmd.Convert for the post-body markdown.
//   - comments: the extracted comment tree, ready to attach to the
//     core.Item.Comments field so the renderer formats it as a proper
//     "## Comments" section instead of leaking inline through the body.
//
// Strategy:
//  1. Parse the raw document.
//  2. Pull comments out *first* (and detach their containers) so they
//     don't get re-rendered as flat text in the body.
//  3. Walk the tree, dropping uninteresting node types and elements
//     matching the class/role denylist.
//  4. Pick the most specific content container — post body for
//     /posts/, profile card for /in/, article body for /pulse/. Fall
//     back to <main> / <body> if none match.
//  5. Serialize the picked subtree back to HTML.
func cleanHTML(raw string) (htmlBody string, comments []core.Comment) {
	doc, err := html.Parse(strings.NewReader(raw))
	if err != nil || doc == nil {
		return raw, nil
	}
	comments = extractComments(doc)
	prune(doc)
	target := pickContent(doc)
	if target == nil {
		target = doc
	}
	var buf bytes.Buffer
	if err := html.Render(&buf, target); err != nil {
		return raw, comments
	}
	return buf.String(), comments
}

// dropTags is removed wholesale — these never contribute reading content
// and often contain large blobs that confuse htmlmd.
var dropTags = map[atom.Atom]bool{
	atom.Script:   true,
	atom.Style:    true,
	atom.Svg:      true,
	atom.Noscript: true,
	atom.Iframe:   true,
	atom.Video:    true,
	atom.Audio:    true,
	atom.Source:   true,
	atom.Track:    true,
	atom.Object:   true,
	atom.Embed:    true,
	atom.Form:     true,
	atom.Button:   true,
	atom.Input:    true,
	atom.Select:   true,
	atom.Textarea: true,
	atom.Label:    true,
	atom.Nav:      true,
	atom.Aside:    true,
	atom.Footer:   true,
	atom.Head:     true,
	atom.Link:     true,
	atom.Meta:     true,
}

// classDenyContains drops any element whose class attribute *contains*
// one of these substrings — covers LinkedIn's hashed/suffixed classes
// like `global-nav__primary-link-me-menu-trigger`.
var classDenyContains = []string{
	// Accessibility duplicates: LinkedIn renders the visible text inside
	// an aria-hidden span and the screen-reader copy in a hidden one.
	// Dropping these removes the "Stephen PimentelStephen Pimentel"
	// pattern at the source.
	"visually-hidden",
	"a11y-text",
	"sr-only",
	"global-nav",
	"feed-identity-module",
	"share-creation",
	"feed-shared-control-menu",
	"feed-shared-actor__sub-description-link",
	"social-actions-bar",
	"reactions-react-button",
	"feed-shared-social-action-bar",
	"social-counts-reactions",
	"feed-shared-mini-update",
	"artdeco-toast",
	"artdeco-modal",
	"sign-in-modal",
	"join-form",
	"public_profile",
	"premium-upsell",
	"pv-profile-section__see-more-inline",
	"profile-photo-edit",
	"contact-info",
	"msg-overlay",
	"chameleon",
	"connections-",
	"pe-ads",
	"premium-",
	"upsell",
	"experiment-",
}

// roleDeny drops elements with these ARIA roles.
var roleDeny = map[string]bool{
	"banner":        true,
	"navigation":    true,
	"complementary": true,
	"dialog":        true,
	"alertdialog":   true,
	"menu":          true,
	"menubar":       true,
	"toolbar":       true,
}

// prune walks the tree and removes nodes matching dropTags / class /
// role denylists. Mutates the tree in place.
func prune(n *html.Node) {
	for c := n.FirstChild; c != nil; {
		next := c.NextSibling
		if shouldDrop(c) {
			n.RemoveChild(c)
		} else {
			prune(c)
		}
		c = next
	}
}

func shouldDrop(n *html.Node) bool {
	if n.Type != html.ElementNode {
		return false
	}
	if dropTags[n.DataAtom] {
		return true
	}
	if role := getAttr(n, "role"); role != "" && roleDeny[strings.ToLower(role)] {
		return true
	}
	if cls := getAttr(n, "class"); cls != "" {
		low := strings.ToLower(cls)
		for _, frag := range classDenyContains {
			if strings.Contains(low, frag) {
				return true
			}
		}
	}
	// LinkedIn pairs each visible text with an aria-hidden duplicate (or
	// vice versa) for screen readers. Either copy alone is fine; keep
	// the visible one and drop aria-hidden ones to deduplicate text.
	if strings.EqualFold(getAttr(n, "aria-hidden"), "true") {
		return true
	}
	// Drop anchors whose href is a LinkedIn UI-control path (edit dialogs,
	// preload modals, overlay images, internal nav). They never produce
	// useful markdown content.
	if n.DataAtom == atom.A {
		href := strings.ToLower(getAttr(n, "href"))
		for _, frag := range hrefDenyContains {
			if strings.Contains(href, frag) {
				return true
			}
		}
		// Empty or "#" anchors that exist only for click handlers.
		if href == "" || href == "#" {
			if !hasMeaningfulText(n) {
				return true
			}
		}
		// Anchors whose only payload is an <img> add no reading value —
		// LinkedIn uses these heavily for thumb wrappers.
		if isImageOnly(n) {
			return true
		}
	}
	// Drop bare images coming from licdn that have no useful alt text.
	// These are profile pics, company logos, etc. that just clutter the
	// markdown without adding information.
	if n.DataAtom == atom.Img {
		alt := strings.TrimSpace(getAttr(n, "alt"))
		src := strings.ToLower(getAttr(n, "src"))
		if (alt == "" || isLowSignalAlt(alt)) && strings.Contains(src, "licdn.com") {
			return true
		}
	}
	return false
}

// hrefDenyContains: anchors whose href contains any of these are UI
// chrome (edit forms, network management, overlays, services blurbs).
var hrefDenyContains = []string{
	"/preload/",
	"/overlay/",
	"/edit/",
	"/services/page/",
	"/mynetwork/",
	"/feed/followers/",
	"/feed/hashtag/",
	"/safety/",
	"/help/",
	"/legal/",
	"/learning/",
	"/premium/",
	"linkedin.com/?",
	"linkedin.com/feed/?",
}

func hasMeaningfulText(n *html.Node) bool {
	return strings.TrimSpace(textOf(n)) != ""
}

func textOf(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return b.String()
}

// isImageOnly reports whether a node has no descendants apart from
// images and whitespace text — i.e. nothing readable to render.
func isImageOnly(n *html.Node) bool {
	hasImg := false
	hasText := false
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			if strings.TrimSpace(n.Data) != "" {
				hasText = true
			}
			return
		}
		if n.Type == html.ElementNode && n.DataAtom == atom.Img {
			hasImg = true
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		walk(c)
	}
	return hasImg && !hasText
}

// isLowSignalAlt covers LinkedIn's stock alt-text noise. These aren't
// content; they're badges/icons/wrapper hints.
func isLowSignalAlt(alt string) bool {
	low := strings.ToLower(alt)
	for _, frag := range []string{
		"profile picture", "profile photo", "company logo", "cover photo",
		"background image", "view ", "edit ", "open ",
	} {
		if strings.Contains(low, frag) {
			return true
		}
	}
	return false
}

// contentSelectors are tried in order; the first match wins. Each entry
// is a predicate over an element node; we prefer post body → profile
// card → pulse article → <main>.
var contentSelectors = []func(*html.Node) bool{
	classExact("feed-shared-update-v2"),
	classContains("feed-shared-update-v2"),
	classContains("update-components-text"),
	classContains("scaffold-finite-scroll__content"),
	classContains("pv-top-card"),
	classContains("scaffold-layout__main"),
	classContains("pulse-article"),
	tagIs(atom.Article),
	tagIs(atom.Main),
}

func pickContent(root *html.Node) *html.Node {
	for _, match := range contentSelectors {
		if n := findFirst(root, match); n != nil {
			return n
		}
	}
	// As a last resort fall back to <body>.
	return findFirst(root, tagIs(atom.Body))
}

func findFirst(n *html.Node, match func(*html.Node) bool) *html.Node {
	if n == nil {
		return nil
	}
	if n.Type == html.ElementNode && match(n) {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if got := findFirst(c, match); got != nil {
			return got
		}
	}
	return nil
}

func classExact(want string) func(*html.Node) bool {
	return func(n *html.Node) bool {
		for _, f := range strings.Fields(getAttr(n, "class")) {
			if f == want {
				return true
			}
		}
		return false
	}
}

func classContains(sub string) func(*html.Node) bool {
	return func(n *html.Node) bool {
		return strings.Contains(strings.ToLower(getAttr(n, "class")), sub)
	}
}

func tagIs(a atom.Atom) func(*html.Node) bool {
	return func(n *html.Node) bool { return n.DataAtom == a }
}

func getAttr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}
