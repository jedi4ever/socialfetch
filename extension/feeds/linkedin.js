/**
 * LinkedIn feed extractor — registers with the socialfetch content script base.
 * Extracts post URLs and HTML from the LinkedIn feed/profile pages.
 */

// Feed post container selectors (LinkedIn changes these periodically)
const LI_FEED_POST_SELECTORS = [
  "div.feed-shared-update-v2",
  "div[data-urn]",
  'div[data-id][class*="feed"]',
];

// Post link selectors within a feed item
const LI_POST_LINK_SELECTORS = [
  'a[href*="/feed/update/"]',
  'a[href*="/posts/"]',
  'a[data-tracking-control-name*="feed"]',
];

// ---------------------------------------------------------------------------
// Register extractors with base content script
// ---------------------------------------------------------------------------

window._socialfetch_feed = window._socialfetch_feed || {};
window._socialfetch_feed.extractUrls = extractLinkedInFeedUrls;
window._socialfetch_feed.extractHtml = extractLinkedInFeedHtml;

// ---------------------------------------------------------------------------
// Extractors
// ---------------------------------------------------------------------------

/**
 * Extract unique post URLs from the LinkedIn feed.
 */
function extractLinkedInFeedUrls() {
  const urls = new Map(); // activity_id → best URL

  // Scan each feed post container
  for (const sel of LI_FEED_POST_SELECTORS) {
    for (const el of document.querySelectorAll(sel)) {
      const urn = el.getAttribute("data-urn") || el.getAttribute("data-id") || "";
      const urnMatch = urn.match(/activity:(\d+)/);
      const activityId = urnMatch ? urnMatch[1] : null;

      // Look for /posts/ links inside this post (preferred — canonical shareable URL)
      let postsUrl = null;
      for (const a of el.querySelectorAll('a[href*="/posts/"]')) {
        const href = a.getAttribute("href") || "";
        if (href.includes("activity") || href.match(/posts\/[^/]+-\d{15,}/)) {
          try {
            postsUrl = new URL(href, window.location.origin).href.split("?")[0];
            break;
          } catch (e) {}
        }
      }

      // Determine key and best URL
      const key = activityId || postsUrl;
      if (!key) continue;

      if (postsUrl) {
        urls.set(key, postsUrl);
      } else if (!urls.has(key)) {
        urls.set(key, `https://www.linkedin.com/feed/update/urn:li:activity:${activityId}/`);
      }
    }
  }

  // Also scan for any /posts/ links not inside feed containers
  for (const a of document.querySelectorAll('a[href*="/posts/"]')) {
    const href = a.getAttribute("href") || "";
    if (href.match(/posts\/[^/]+-(?:activity|ugcPost)-\d{15,}/)) {
      try {
        const url = new URL(href, window.location.origin).href.split("?")[0];
        const idMatch = href.match(/(?:activity|ugcPost)-(\d{15,})/);
        if (idMatch && !urls.has(idMatch[1])) {
          urls.set(idMatch[1], url);
        }
      } catch (e) {}
    }
  }

  return Array.from(urls.values());
}

/**
 * Extract feed posts with their HTML and metadata.
 */
function extractLinkedInFeedHtml() {
  const posts = [];
  const seen = new Set();

  for (const sel of LI_FEED_POST_SELECTORS) {
    for (const el of document.querySelectorAll(sel)) {
      const urn = el.getAttribute("data-urn") || el.getAttribute("data-id") || "";
      const activityMatch = urn.match(/activity:(\d+)/);
      const activityId = activityMatch ? activityMatch[1] : null;

      const key = activityId || el.outerHTML.slice(0, 200);
      if (seen.has(key)) continue;
      seen.add(key);

      // Find the post URL within this element
      let postUrl = null;
      for (const linkSel of LI_POST_LINK_SELECTORS) {
        const link = el.querySelector(linkSel);
        if (link) {
          const href = link.getAttribute("href") || "";
          try {
            postUrl = new URL(href, window.location.origin).href.split("?")[0];
          } catch (e) { /* skip */ }
          break;
        }
      }

      if (!postUrl && activityId) {
        postUrl = `https://www.linkedin.com/feed/update/urn:li:activity:${activityId}/`;
      }

      posts.push({
        activity_id: activityId,
        url: postUrl,
        html: el.outerHTML,
        text: el.innerText.slice(0, 2000),
      });
    }
  }

  return posts;
}
