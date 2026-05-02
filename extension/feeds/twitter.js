/**
 * Twitter/X feed extractor — registers with the socialfetch content script base.
 * Extracts tweet URLs and HTML from the X.com timeline.
 */

// Tweet article selectors (X uses article elements for tweets)
const X_TWEET_SELECTORS = [
  'article[data-testid="tweet"]',
  "article[role='article']",
];

// ---------------------------------------------------------------------------
// Register extractors with base content script
// ---------------------------------------------------------------------------

window._socialfetch_feed = window._socialfetch_feed || {};
window._socialfetch_feed.extractUrls = extractTwitterFeedUrls;
window._socialfetch_feed.extractHtml = extractTwitterFeedHtml;

// ---------------------------------------------------------------------------
// Extractors
// ---------------------------------------------------------------------------

/**
 * Extract unique tweet URLs from the X/Twitter timeline.
 */
function extractTwitterFeedUrls() {
  const urls = new Set();

  for (const sel of X_TWEET_SELECTORS) {
    for (const article of document.querySelectorAll(sel)) {
      // X tweet links follow the pattern /<username>/status/<id>
      const timeLinks = article.querySelectorAll('a[href*="/status/"] time');
      for (const time of timeLinks) {
        const a = time.closest("a");
        if (a) {
          const href = a.getAttribute("href") || "";
          try {
            const url = new URL(href, window.location.origin);
            // Only include direct tweet links (not quote tweets or other embeds)
            if (/^\/[^/]+\/status\/\d+$/.test(url.pathname)) {
              urls.add(url.href);
            }
          } catch (e) {
            // skip
          }
        }
      }
    }
  }

  return Array.from(urls);
}

/**
 * Extract tweets with their HTML and metadata.
 */
function extractTwitterFeedHtml() {
  const posts = [];
  const seen = new Set();

  for (const sel of X_TWEET_SELECTORS) {
    for (const article of document.querySelectorAll(sel)) {
      // Find tweet URL via the timestamp link
      let tweetUrl = null;
      let tweetId = null;
      const timeLink = article.querySelector('a[href*="/status/"] time');
      if (timeLink) {
        const a = timeLink.closest("a");
        if (a) {
          const href = a.getAttribute("href") || "";
          try {
            const url = new URL(href, window.location.origin);
            if (/^\/[^/]+\/status\/\d+$/.test(url.pathname)) {
              tweetUrl = url.href;
              tweetId = url.pathname.split("/").pop();
            }
          } catch (e) { /* skip */ }
        }
      }

      const key = tweetId || article.innerText.slice(0, 200);
      if (seen.has(key)) continue;
      seen.add(key);

      posts.push({
        tweet_id: tweetId,
        url: tweetUrl,
        html: article.outerHTML,
        text: article.innerText.slice(0, 2000),
      });
    }
  }

  return posts;
}
