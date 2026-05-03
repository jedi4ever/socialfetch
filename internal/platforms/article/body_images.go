package article

// Body-image extraction — finds <img> tags inside a parsed article
// page that look like content imagery (post-attached photos, embedded
// figures) rather than chrome (ad slots, related-post thumbnails,
// author headshots, decorative SVGs).
//
// Per-platform fetchers call AppendBodyImages with their own host
// matcher: Medium passes a closure that accepts cdn-images-1.medium.com
// and miro.medium.com hosts; Substack passes substackcdn.com /
// bucketeer-* / *.cloudfront.net. The hero image already extracted
// by BaseFromPage (from og:image / JSON-LD) is deduped automatically
// so we don't double-count it.
//
// The same SOCIAL_FETCH_MIN_IMAGE_SIZE env var the LinkedIn extractor
// honours applies here — operators can tune one knob and have it
// affect every platform's body-image extraction.

import (
	"net/url"
	"os"
	"strconv"
	"strings"

	"golang.org/x/net/html"

	"github.com/jedi4ever/social-skills/internal/core"
	"github.com/jedi4ever/social-skills/internal/util/htmlmeta"
)

// MediaDedupKey returns a stable identity for an image URL so that
// resolution variants of the same underlying image collapse to one
// entry in Item.Media. Exported because LinkedIn's per-platform
// extractor (different package) reuses it for the same purpose.
//
// The strategy is "last path segment after URL-decoding any embedded
// URLs" — works for the three patterns we hit in real life:
//
//   - Medium: .../v2/resize:fit:1200/1*xxx.jpeg → 1*xxx.jpeg
//   - Medium: .../v2/resize:fit:2000/1*xxx.jpeg → 1*xxx.jpeg (dedups)
//   - Substack: ...image/fetch/$s_!shiQ!,w_1200,h_675,.../https%3A%2F%2F...
//     → unwrap embedded URL → final segment
//   - LinkedIn: .../feedshare-shrink_800/B56.../0/1777408874005?...
//     → 1777408874005 (same across resolution variants)
//   - Anything else: full URL (no dedup, safe default)
//
// When two URLs produce the same key, the FIRST one seen wins —
// callers can sort the input order to prefer higher resolutions.
func MediaDedupKey(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	path := u.Path
	// Substack-style fetch URLs embed the source URL inside the
	// path (URL-encoded). Unwrap once so the dedup key is the
	// underlying file rather than the wrapper transform.
	if i := strings.Index(path, "https%3A"); i >= 0 {
		if decoded, derr := url.PathUnescape(path[i:]); derr == nil && decoded != path[i:] {
			return MediaDedupKey(decoded)
		}
	}
	if i := strings.LastIndex(path, "/"); i >= 0 && i < len(path)-1 {
		return path[i+1:]
	}
	if path != "" {
		return path
	}
	return rawURL
}

const minImageSizeEnv = "SOCIAL_FETCH_MIN_IMAGE_SIZE"

// minImageSize returns the configured smallest-image threshold.
// Default 64px on either axis — anything smaller is almost always an
// icon / reaction badge / tracking pixel rather than article content.
func minImageSize() int {
	if v := strings.TrimSpace(os.Getenv(minImageSizeEnv)); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 64
}

// HostMatcher is a per-platform filter — given an image src URL,
// returns true if the URL's host is one this platform considers
// "real content media" (post-CDN host) vs unrelated third-party
// domains. Platforms supply their own; the helper only walks once
// and applies the matcher to each candidate.
type HostMatcher func(src string) bool

// AppendBodyImages walks the parsed article DOM, finds every <img>
// inside a body container (selectors arg), filters via the host
// matcher + chrome denylist + size threshold, and appends the
// surviving images to item.Media. Idempotent: dedupes against
// Media URLs already on the item (so the hero from BaseFromPage
// doesn't get re-added when it appears in the body).
//
// The selectors arg is the same priority-ordered list each
// extractor passes to RenderArticle — picks the article container
// first, falls back to <body> when none match.
func AppendBodyImages(item *core.Item, page *htmlmeta.Page, selectors []string, host HostMatcher) {
	if item == nil || page == nil || page.Doc == nil || host == nil {
		return
	}
	root := pickArticleRoot(page, selectors)
	if root == nil {
		return
	}
	// Dedup by stable identity (MediaDedupKey) rather than by full
	// URL so resolution variants of the same image (Medium's
	// resize:fit:1200 vs resize:fit:2000) collapse to a single
	// entry. Pre-seed with whatever's already on item.Media so the
	// hero from BaseFromPage doesn't get duplicated.
	seen := map[string]bool{}
	for _, m := range item.Media {
		seen[MediaDedupKey(m.URL)] = true
	}
	threshold := minImageSize()
	walkImages(root, func(n *html.Node) {
		src := bestImageSrc(n)
		if src == "" || !host(src) {
			return
		}
		key := MediaDedupKey(src)
		if seen[key] {
			return
		}
		if isChromeImage(n) {
			return
		}
		if isUnderThreshold(n, threshold) {
			return
		}
		item.Media = append(item.Media, core.Media{
			URL:  src,
			Type: "image",
			Alt:  cleanAlt(getAttr(n, "alt")),
		})
		seen[key] = true
	})
}

// pickArticleRoot resolves the article container the same way
// RenderArticle does. Returns the first selector that matches,
// or page.Doc as a last resort.
func pickArticleRoot(page *htmlmeta.Page, selectors []string) *html.Node {
	for _, sel := range selectors {
		if n := htmlmeta.SelectFirst(page.Doc, sel); n != nil {
			return n
		}
	}
	return page.Doc
}

// walkImages calls fn on every <img> descendant of n.
func walkImages(n *html.Node, fn func(*html.Node)) {
	if n.Type == html.ElementNode && n.Data == "img" {
		fn(n)
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		walkImages(c, fn)
	}
}

// bestImageSrc returns the most likely real URL for an <img>. Some
// platforms lazy-load with a 1×1 placeholder gif on `src` and the
// real URL on `data-src` / `data-srcset` / `srcset`; we check those
// in priority order. Falls back to `src` when no lazy attr present.
func bestImageSrc(n *html.Node) string {
	for _, attr := range []string{"data-src", "data-original", "data-lazy-src"} {
		if v := strings.TrimSpace(getAttr(n, attr)); v != "" {
			return v
		}
	}
	if v := strings.TrimSpace(getAttr(n, "srcset")); v != "" {
		// srcset is "url1 1x, url2 2x" — pick the FIRST URL (1x is
		// typically the smallest, fine for our metadata purposes).
		if i := strings.IndexAny(v, " ,"); i > 0 {
			return v[:i]
		}
		return v
	}
	return strings.TrimSpace(getAttr(n, "src"))
}

// isChromeImage applies a class-substring chrome filter — drops
// images whose ancestor classes contain known UI markers. Conservative
// so legitimate body images don't get accidentally dropped.
func isChromeImage(n *html.Node) bool {
	for cur := n; cur != nil; cur = cur.Parent {
		if cur.Type != html.ElementNode {
			continue
		}
		class := strings.ToLower(getAttr(cur, "class"))
		if class == "" {
			continue
		}
		for _, deny := range bodyImageChromeDeny {
			if strings.Contains(class, deny) {
				return true
			}
		}
	}
	return false
}

// bodyImageChromeDeny — class-substring fragments common across
// blog platforms that mark an image as chrome. Not platform-
// specific (Medium / Substack / generic articles share most
// chrome patterns thanks to common CSS conventions).
var bodyImageChromeDeny = []string{
	"avatar",
	"author-avatar",
	"author-image",
	"author-photo",
	"profile-image",
	"profile-photo",
	"site-logo",
	"site-icon",
	"footer",
	"header-",
	"nav-",
	"sidebar",
	"related-",
	"recommended",
	"sponsored",
	"ad-",
	"advertisement",
	"share-",
	"social-share",
	"comment-",
	"reactions",
	"icon-",
	"emoji",
}

// isUnderThreshold returns true when the image's width/height
// attributes are at or below threshold px. Returns false when the
// image has no dimension hints — we can't drop something we don't
// know the size of (would risk losing real body images served
// without explicit dimensions).
func isUnderThreshold(n *html.Node, threshold int) bool {
	for _, dim := range []string{"width", "height"} {
		v := strings.TrimSpace(getAttr(n, dim))
		if v == "" {
			continue
		}
		if px, err := strconv.Atoi(v); err == nil && px > 0 && px <= threshold {
			return true
		}
	}
	return false
}

// cleanAlt drops empty / placeholder alt text. Keeps real
// descriptive alt text so an agent can OCR-skip the image when the
// alt already says what's in it.
func cleanAlt(alt string) string {
	alt = strings.TrimSpace(alt)
	low := strings.ToLower(alt)
	switch low {
	case "", "image", "photo", "picture", "img":
		return ""
	}
	return alt
}

// getAttr — small helper, returns the first matching attribute
// value or empty string. Lives here rather than imported to avoid
// pulling in another package for a 5-line function.
func getAttr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}
