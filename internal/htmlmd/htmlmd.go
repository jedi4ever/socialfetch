// Package htmlmd converts HTML fragments into clean markdown. It handles
// the elements article fetchers commonly see — headings, paragraphs,
// lists, blockquotes, code, links, images, line breaks — and skips the
// rest (script, style, nav chrome) silently.
//
// This is intentionally a small, dependency-light implementation. Real
// readability extraction is out of scope; pair this with htmlmeta.Page's
// article container detection for usable output.
package htmlmd

import (
	"strings"

	"golang.org/x/net/html"
)

// Convert returns markdown for the given HTML fragment.
func Convert(htmlFragment string) string {
	root, err := html.Parse(strings.NewReader("<div>" + htmlFragment + "</div>"))
	if err != nil {
		return ""
	}
	var b strings.Builder
	render(root, &b, ctx{})
	return collapseBlankLines(strings.TrimSpace(b.String()))
}

type ctx struct {
	listKind     string // "ul" or "ol"
	listIndex    int    // 1-based for "ol"
	indent       string
	inPre        bool
	suppressText bool
}

func render(n *html.Node, b *strings.Builder, c ctx) {
	switch n.Type {
	case html.TextNode:
		if c.suppressText {
			return
		}
		b.WriteString(escapeMarkdown(n.Data, c.inPre))
		return
	case html.ElementNode:
		switch n.Data {
		case "script", "style", "nav", "aside", "footer", "form", "iframe", "noscript":
			return
		case "h1", "h2", "h3", "h4", "h5", "h6":
			level := int(n.Data[1] - '0')
			b.WriteString("\n\n" + strings.Repeat("#", level) + " ")
			renderChildren(n, b, c)
			b.WriteString("\n\n")
			return
		case "p":
			b.WriteString("\n\n")
			renderChildren(n, b, c)
			b.WriteString("\n\n")
			return
		case "br":
			b.WriteString("  \n")
			return
		case "hr":
			b.WriteString("\n\n---\n\n")
			return
		case "strong", "b":
			b.WriteString("**")
			renderChildren(n, b, c)
			b.WriteString("**")
			return
		case "em", "i":
			b.WriteString("*")
			renderChildren(n, b, c)
			b.WriteString("*")
			return
		case "code":
			if c.inPre {
				renderChildren(n, b, c)
				return
			}
			b.WriteString("`")
			renderChildren(n, b, c)
			b.WriteString("`")
			return
		case "pre":
			b.WriteString("\n\n```\n")
			cc := c
			cc.inPre = true
			renderChildren(n, b, cc)
			b.WriteString("\n```\n\n")
			return
		case "blockquote":
			var inner strings.Builder
			renderChildren(n, &inner, c)
			for _, line := range strings.Split(strings.TrimSpace(inner.String()), "\n") {
				b.WriteString("\n> ")
				b.WriteString(line)
			}
			b.WriteString("\n\n")
			return
		case "ul", "ol":
			cc := c
			cc.listKind = n.Data
			cc.listIndex = 1
			cc.indent = c.indent + "  "
			b.WriteString("\n")
			for child := n.FirstChild; child != nil; child = child.NextSibling {
				if child.Type == html.ElementNode && child.Data == "li" {
					b.WriteString(c.indent)
					if cc.listKind == "ol" {
						b.WriteString(itoa(cc.listIndex) + ". ")
						cc.listIndex++
					} else {
						b.WriteString("- ")
					}
					var item strings.Builder
					renderChildren(child, &item, cc)
					b.WriteString(strings.TrimSpace(item.String()))
					b.WriteString("\n")
				}
			}
			b.WriteString("\n")
			return
		case "a":
			href := getAttr(n, "href")
			if href == "" {
				renderChildren(n, b, c)
				return
			}
			b.WriteString("[")
			renderChildren(n, b, c)
			b.WriteString("](")
			b.WriteString(href)
			b.WriteString(")")
			return
		case "img":
			src := getAttr(n, "src")
			alt := getAttr(n, "alt")
			if src != "" {
				b.WriteString("![" + alt + "](" + src + ")")
			}
			return
		}
	}
	renderChildren(n, b, c)
}

func renderChildren(n *html.Node, b *strings.Builder, c ctx) {
	for child := n.FirstChild; child != nil; child = child.NextSibling {
		render(child, b, c)
	}
}

func getAttr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

// escapeMarkdown escapes characters that would otherwise start markdown
// constructs, except inside <pre> where we want the original content.
func escapeMarkdown(s string, inPre bool) string {
	if inPre {
		return s
	}
	r := strings.NewReplacer(
		`\`, `\\`,
		"`", "\\`",
		"*", `\*`,
		"_", `\_`,
	)
	return r.Replace(s)
}

func collapseBlankLines(s string) string {
	for strings.Contains(s, "\n\n\n") {
		s = strings.ReplaceAll(s, "\n\n\n", "\n\n")
	}
	return s
}

func itoa(n int) string {
	// strconv.Itoa avoided to keep the import surface minimal.
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
