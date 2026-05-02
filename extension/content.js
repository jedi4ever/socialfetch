/**
 * Base content script — injected into all pages managed by socialfetch.
 * Handles generic actions that work on any site.
 *
 * Site-specific feed extraction is in feeds/*.js — those scripts register
 * themselves via window._socialfetch_feed so the base handler can delegate.
 *
 * Actions:
 *   - get_html      → return full page HTML, URL, title
 *   - scroll         → scroll the page by a given amount
 *   - get_feed       → delegate to site-specific feed extractor
 *   - get_feed_html  → delegate to site-specific feed extractor
 */

// Registry for site-specific feed extractors.
// Feed scripts (feeds/linkedin.js, feeds/twitter.js) register here.
window._socialfetch_feed = window._socialfetch_feed || {
  extractUrls: null,
  extractHtml: null,
};

// Keep the background service worker alive by holding a port open.
// Chrome kills the SW after 30s of inactivity; an open port prevents that.
(function keepAlive() {
  try {
    const port = chrome.runtime.connect({ name: "keepalive" });
    port.onDisconnect.addListener(() => {
      // Port auto-closes after ~5 min; reopen it
      setTimeout(keepAlive, 1000);
    });
  } catch (e) {}
})();


// ---------------------------------------------------------------------------
// Message handler
// ---------------------------------------------------------------------------

chrome.runtime.onMessage.addListener((msg, sender, sendResponse) => {
  const { action } = msg;

  switch (action) {
    case "get_html":
      sendResponse({
        html: document.documentElement.outerHTML,
        url: window.location.href,
        title: document.title,
      });
      break;

    case "scroll": {
      // Three-tier fallback. Most pages scroll the document body
      // (handled by window.scrollBy). React SPAs like LinkedIn's new
      // SDUI search results scroll an inner overflow:auto container
      // — for those, window.scrollBy is a no-op (we observed scrollY
      // stuck at 0 even after multiple commands). We try the simple
      // path first, then escalate to scrollingElement, then hunt for
      // the largest scrollable element.
      const amount = msg.amount || 1000;
      const before = window.scrollY;
      window.scrollBy(0, amount);

      // After window.scrollBy: did the document actually move?
      let moved = window.scrollY !== before;
      let scroller = null;
      if (!moved) {
        // Try the modern scrolling element (HTML5 spec).
        scroller = document.scrollingElement || document.documentElement;
        const beforeS = scroller.scrollTop;
        scroller.scrollBy(0, amount);
        moved = scroller.scrollTop !== beforeS;
      }
      if (!moved) {
        // Last resort: find the largest visible element with
        // overflow-y: auto/scroll that has content beyond its
        // viewport. SPAs typically have ONE such container holding
        // their main content area.
        scroller = findLargestScrollable();
        if (scroller) scroller.scrollBy(0, amount);
      }
      sendResponse({
        scrollY: window.scrollY,
        innerScrollTop: scroller ? scroller.scrollTop : 0,
        // Debug fingerprint of which element we ended up scrolling.
        // Empty when the document body/scrollingElement handled it.
        scrollerInfo: scroller
          ? {
              tag: scroller.tagName,
              id: scroller.id || "",
              cls: (scroller.className || "").slice(0, 80),
              scrollHeight: scroller.scrollHeight,
              clientHeight: scroller.clientHeight,
            }
          : null,
      });
      break;
    }

    case "get_feed": {
      const extractor = window._socialfetch_feed.extractUrls;
      if (extractor) {
        sendResponse({ posts: extractor() });
      } else {
        // Fallback: extract all links from the page
        sendResponse({ posts: extractAllLinks() });
      }
      break;
    }

    case "get_feed_html": {
      const extractor = window._socialfetch_feed.extractHtml;
      if (extractor) {
        sendResponse({ posts: extractor() });
      } else {
        sendResponse({ posts: [], error: "No feed extractor for this site" });
      }
      break;
    }

    default:
      sendResponse({ error: `Unknown action: ${action}` });
  }

  return true;
});

// ---------------------------------------------------------------------------
// Generic link extraction fallback
// ---------------------------------------------------------------------------

/**
 * Find the largest visible element on the page with vertical overflow
 * and content past its viewport. Used as a fallback for the scroll
 * command on SPAs (LinkedIn's new SDUI, etc.) that scroll an inner
 * container rather than the document body.
 *
 * "Largest" = biggest clientWidth × clientHeight. Picks the dominant
 * content region in nearly all cases. We skip elements with tiny
 * dimensions (<200×200) to avoid sidebar widgets and toolbars.
 */
function findLargestScrollable() {
  let best = null;
  let bestArea = 0;
  const all = document.querySelectorAll("*");
  for (const el of all) {
    if (el.clientWidth < 200 || el.clientHeight < 200) continue;
    if (el.scrollHeight <= el.clientHeight + 1) continue;
    const style = getComputedStyle(el);
    const overflow = style.overflowY;
    if (overflow !== "auto" && overflow !== "scroll" && overflow !== "overlay") continue;
    const area = el.clientWidth * el.clientHeight;
    if (area > bestArea) {
      best = el;
      bestArea = area;
    }
  }
  return best;
}

/**
 * Extract all unique links from the page as a simple fallback
 * when no site-specific feed extractor is available.
 */
function extractAllLinks() {
  const urls = new Set();
  for (const a of document.querySelectorAll("a[href]")) {
    const href = a.getAttribute("href") || "";
    if (!href || href.startsWith("#") || href.startsWith("javascript:")) continue;
    try {
      const url = new URL(href, window.location.origin);
      if (url.protocol.startsWith("http")) {
        urls.add(url.href.split("#")[0]);
      }
    } catch (e) {
      // skip invalid URLs
    }
  }
  return Array.from(urls);
}
