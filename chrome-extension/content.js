/**
 * Base content script — injected into all pages managed by socialfetch.
 * Handles generic actions that work on any site.
 *
 * All page-content extraction happens server-side in the Go binary
 * (internal/platforms/<site>/{fetch,timeline,search}_extract.go).
 * The extension's only job is to hand back rendered HTML and to
 * drive scroll / wheel events that trigger lazy loads.
 *
 * Actions:
 *   - get_html           → return full page HTML, URL, title
 *   - scroll             → scroll the largest scrollable element
 *   - scroll_to_bottom   → snap to the bottom of the largest scroller
 *   - wheel              → dispatch a synthetic wheel event
 *   - enumerate_scrollers → diagnostic: list every scrollable element
 */

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

    case "scroll_to_bottom": {
      // Move the largest scrollable element all the way to its
      // current bottom. No amount math needed — viewport-independent
      // by construction. Used by lazy-load loops where the goal is
      // "hit the bottom and dwell" so an IntersectionObserver fires.
      // Returns the scroller's clientHeight so callers can size
      // follow-up wheel events relative to the viewport.
      let scroller = findLargestScrollable();
      if (!scroller) {
        // Fallback to document scroller for vanilla pages.
        scroller = document.scrollingElement || document.documentElement;
      }
      const target = scroller.scrollHeight - scroller.clientHeight;
      scroller.scrollTop = target;
      sendResponse({
        scrollTop: scroller.scrollTop,
        scrollHeight: scroller.scrollHeight,
        clientHeight: scroller.clientHeight,
        atBottom: scroller.scrollHeight - scroller.scrollTop - scroller.clientHeight < 2,
      });
      break;
    }

    case "enumerate_scrollers": {
      // Diagnostic: list every element on the page that has vertical
      // overflow and content past its viewport. Used to confirm the
      // multi-tier scroll handler is picking the right container —
      // if a SPA's lazy-load lives behind a different scrollable
      // element than the one we picked, this surfaces it.
      const out = [];
      for (const el of document.querySelectorAll("*")) {
        if (el.scrollHeight <= el.clientHeight + 1) continue;
        const style = getComputedStyle(el);
        if (!["auto", "scroll", "overlay"].includes(style.overflowY)) continue;
        out.push({
          tag: el.tagName,
          id: el.id || "",
          cls: (el.className || "").toString().slice(0, 80),
          scrollHeight: el.scrollHeight,
          clientHeight: el.clientHeight,
          scrollTop: el.scrollTop,
          overflowY: style.overflowY,
        });
      }
      sendResponse({ scrollers: out });
      break;
    }

    case "wheel": {
      // Dispatch a synthetic wheel event at the center of the
      // viewport. Some SPAs (LinkedIn's new SDUI seems to be one)
      // lazy-load on real wheel events but ignore programmatic
      // scrollBy calls — likely because their IntersectionObserver
      // sentinel is watching for natural scroll dynamics. Wheel
      // events carry deltaY which matches user-initiated scrolls
      // more closely than scrollBy and bubble through React's
      // event delegation.
      const deltaY = msg.deltaY || 1000;
      const x = window.innerWidth / 2;
      const y = window.innerHeight / 2;
      const target = document.elementFromPoint(x, y) || document.body;
      target.dispatchEvent(
        new WheelEvent("wheel", {
          deltaY: deltaY,
          deltaMode: 0, // pixels
          bubbles: true,
          cancelable: true,
          view: window,
        })
      );
      sendResponse({ deltaY, scrollY: window.scrollY });
      break;
    }

    default:
      sendResponse({ error: `Unknown action: ${action}` });
  }

  return true;
});

// ---------------------------------------------------------------------------
// Helpers
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

