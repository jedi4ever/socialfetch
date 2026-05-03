package linkedin

import (
	"os"
	"strings"

	"golang.org/x/net/html"

	"github.com/jedi4ever/social-skills/internal/core"
)

// SOCIAL_FETCH_MIN_IMAGE_SIZE controls the smallest image dimension
// (px, on either axis) that survives the chrome filter. Defaults to
// 64 — anything smaller is almost always an icon, reaction badge, or
// tracking pixel rather than post media. Operators can bump it
// higher to drop larger thumbnails / company logos
// (`SOCIAL_FETCH_MIN_IMAGE_SIZE=200` keeps only post-photo-sized
// content) or lower to capture small-but-meaningful images.
const minImageSizeEnv = "SOCIAL_FETCH_MIN_IMAGE_SIZE"

// minImageSize returns the configured threshold, falling back to 64
// when the env var is unset / non-numeric. Read every call rather
// than cached so tests using t.Setenv exercise different values
// without restart.
func minImageSize() int {
	if v := strings.TrimSpace(os.Getenv(minImageSizeEnv)); v != "" {
		if n := atoi(v); n > 0 {
			return n
		}
	}
	return 64
}

// extractMedia walks a parsed LinkedIn post DOM and pulls out the
// images that are part of the post itself (attached photos, shared-
// link thumbnails, video posters), filtering out the chrome — profile
// avatars, company logos, decorative SVGs, reaction icons, etc.
//
// The signal we use to separate "real post media" from "UI chrome":
//
//  1. Source must be on `media.licdn.com` (that's where post images +
//     video posters live; profile pics are at `static.licdn.com`).
//  2. Element must NOT be inside a class containing the chrome
//     denylist (avatar / actor-image / reaction / brand). LinkedIn's
//     class names are hashed but always contain semantic substrings.
//  3. Image dimension hints (when present): drop anything ≤ 64px on
//     either axis — those are reaction badges or profile thumbnails.
//
// Returns []core.Media with `Type: "image"` and `Alt` populated from
// the alt attribute when meaningful. Empty slice when the post has no
// attached media. Note: this runs against the RAW HTML before
// cleanHTML strips chrome — cleanHTML drops images aggressively, so
// we extract first.
func extractMedia(doc *html.Node) []core.Media {
	if doc == nil {
		return nil
	}
	var out []core.Media
	seen := map[string]bool{}
	var walk func(*html.Node, []string)
	walk = func(n *html.Node, ancestorClasses []string) {
		if n.Type == html.ElementNode {
			class := getAttr(n, "class")
			if class != "" {
				ancestorClasses = append(ancestorClasses, strings.ToLower(class))
			}
			if n.Data == "img" {
				if m, ok := mediaFromImg(n, ancestorClasses); ok && !seen[m.URL] {
					out = append(out, m)
					seen[m.URL] = true
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c, ancestorClasses)
		}
	}
	walk(doc, nil)
	return out
}

// mediaFromImg decides whether a single <img> element is post media
// worth surfacing, and returns the corresponding core.Media. The
// boolean is false when the image is chrome (profile avatar, reaction
// badge, decorative tracker pixel) or when the src doesn't look like
// licdn-served content.
func mediaFromImg(n *html.Node, ancestorClasses []string) (core.Media, bool) {
	src := strings.TrimSpace(getAttr(n, "src"))
	// LinkedIn lazy-loads images; the real URL is on data-delayed-url
	// or data-li-src when src is a 1x1 placeholder gif.
	for _, attr := range []string{"data-delayed-url", "data-li-src", "data-src"} {
		if v := strings.TrimSpace(getAttr(n, attr)); v != "" {
			src = v
			break
		}
	}
	if src == "" {
		return core.Media{}, false
	}
	// Only `media.licdn.com` (post media + video posters). Profile
	// pictures live on `static.licdn.com`; we drop those.
	if !strings.Contains(src, "media.licdn.com") &&
		!strings.Contains(src, "media-exp") &&
		!strings.Contains(src, "/dms/image/") {
		return core.Media{}, false
	}
	// URL-path filter: even on media.licdn.com, the `/profile-
	// displayphoto-shrink_*/` path serves user avatars and the
	// `/profile-framedphoto-shrink_*/` path serves "people you
	// might know" thumbnails. The class-substring chrome filter
	// catches most of these via ancestor class, but LinkedIn
	// sometimes nests the avatar inside a generic update card
	// where the class chain doesn't carry an obvious avatar
	// signal. URL-path filtering is the reliable fallback.
	for _, mark := range []string{"profile-displayphoto", "profile-framedphoto", "company-logo", "ghost-person"} {
		if strings.Contains(src, mark) {
			return core.Media{}, false
		}
	}
	// Chrome filter — class-string substring match across the
	// ancestor chain.
	for _, c := range ancestorClasses {
		for _, deny := range mediaChromeDeny {
			if strings.Contains(c, deny) {
				return core.Media{}, false
			}
		}
	}
	// Skip tiny thumbnails / icons.
	if isTinyImage(n) {
		return core.Media{}, false
	}
	alt := strings.TrimSpace(getAttr(n, "alt"))
	if isLowSignalAlt(alt) {
		alt = ""
	}
	// LinkedIn sometimes nests the post media inside a poster container
	// for a video — detect that and label accordingly. The poster IS an
	// image; we surface the image URL but tag it as `video-poster` so
	// the agent knows there's video content (LinkedIn doesn't expose
	// the .mp4 URL outside the player JS, which we can't reach without
	// a headless browser).
	mediaType := "image"
	for _, c := range ancestorClasses {
		if strings.Contains(c, "video") || strings.Contains(c, "media-player") {
			mediaType = "video-poster"
			break
		}
	}
	return core.Media{
		URL:  src,
		Type: mediaType,
		Alt:  alt,
	}, true
}

// mediaChromeDeny lists class-substring fragments that mark an image
// as UI chrome rather than post content. Tuned against the current
// LinkedIn DOM — will need updating when LinkedIn rotates class
// hashes (track via the unit tests in fetch_extract_media_test.go).
//
// Entries with trailing `-` or `_` enforce a word-boundary so we
// don't false-positive on roots like LinkedIn's `<html
// class="icons-loaded">` (which would otherwise be matched by a
// bare `icon` substring and kill every image in the document).
var mediaChromeDeny = []string{
	"avatar",         // any avatar variant
	"actor-image",    // post author thumbnail
	"actor__avatar",  // post author thumbnail (variant)
	"presence-",      // online-status indicator overlay (strict prefix)
	"badge-",         // reaction badge / verification badge (strict prefix)
	"reaction-",      // like/celebrate/etc reaction icon (strict prefix)
	"social-detail",  // reaction summary row
	"comment-",       // images inside comments (separate from post)
	"reply-",         // replies-thread images
	"company-logo",   // shared-link source logo
	"organization-",  // company branding
	"emoji",          // unicode-ish emoji image
	"global-nav",     // top nav avatars / logos
	"profile-photo",  // explicit profile photo class
	"profile-displayphoto", // licdn-served profile picture path
	"recommendation", // "people also viewed" thumbnails
	"author-card",    // article-author headshot
}

// isTinyImage returns true for images whose width / height attribute
// is at or below the configured min-size threshold (default 64px,
// override via SOCIAL_FETCH_MIN_IMAGE_SIZE), OR whose URL carries a
// LinkedIn sprite/icon-shaped sizing hint. Reaction badges and online
// indicators are typically 16-24px; profile thumbs are 48-64px. Real
// post media is 400px+.
//
// Reuses the package-level atoi helper from timeline_extract.go —
// returns 0 for non-numeric strings, treated as "no dimension hint".
func isTinyImage(n *html.Node) bool {
	threshold := minImageSize()
	for _, dim := range []string{"width", "height"} {
		v := strings.TrimSpace(getAttr(n, dim))
		if v == "" {
			continue
		}
		if px := atoi(v); px > 0 && px <= threshold {
			return true
		}
	}
	src := getAttr(n, "src")
	// LinkedIn's URL-based size hints — these stay hardcoded
	// because they're shape-based (sprite path, fixed-size CDN
	// transforms) rather than pixel-based. The configurable
	// threshold above handles the common case.
	for _, marker := range []string{"_h_48,", "_h_64,", "_w_48,", "_w_64,",
		"=h64-", "=w64-", "/sc/h/"} {
		if strings.Contains(src, marker) {
			return true
		}
	}
	return false
}
