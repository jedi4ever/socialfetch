/**
 * Background service worker — maintains WebSocket connection to social-fetch server
 * and dispatches commands to content scripts on any supported site.
 *
 * Uses chrome.alarms to periodically check and reconnect the WebSocket,
 * since Chrome MV3 kills service workers after 30s of inactivity.
 */

const DEFAULT_SERVER = "ws://127.0.0.1:5555/ws/extension";

const TAB_GROUP_TITLE = "social-fetch";
const TAB_GROUP_COLOR = "blue";

let ws = null;
let reconnectAttempts = 0;
const MAX_RECONNECT_ATTEMPTS = 100;
let connectionState = "disconnected";
let tabGroupId = null;

// ---------------------------------------------------------------------------
// State helpers
// ---------------------------------------------------------------------------

function setState(state) {
  connectionState = state;
  chrome.storage.local.set({ connectionState: state });
}

function getServerUrl() {
  return new Promise((resolve) => {
    chrome.storage.local.get(["serverUrl"], (result) => {
      resolve(result.serverUrl || DEFAULT_SERVER);
    });
  });
}

function getSettings() {
  return new Promise((resolve) => {
    chrome.storage.local.get(["settings"], (result) => {
      resolve(result.settings || {});
    });
  });
}

// ---------------------------------------------------------------------------
// WebSocket connection (with alarm-based reconnection)
// ---------------------------------------------------------------------------

// Alarm checks connection and reconnects if needed
chrome.alarms.onAlarm.addListener((alarm) => {
  if (alarm.name === "checkConnection") {
    console.log("[social-fetch] Alarm fired, ws state:", ws ? ws.readyState : "null");
    if (!ws || ws.readyState === WebSocket.CLOSED || ws.readyState === WebSocket.CLOSING) {
      connect();
    }
  }
});

// Create the alarm (also re-created on every startup/install)
chrome.alarms.create("checkConnection", { periodInMinutes: 0.1 });
console.log("[social-fetch] Alarm created");

async function connect() {
  if (ws && (ws.readyState === WebSocket.CONNECTING || ws.readyState === WebSocket.OPEN)) {
    return;
  }

  const serverUrl = await getServerUrl();
  setState("connecting");
  console.log(`[social-fetch] Connecting to ${serverUrl}...`);

  try {
    ws = new WebSocket(serverUrl);
  } catch (err) {
    console.error("[social-fetch] WebSocket constructor error:", err);
    setState("disconnected");
    ws = null;
    return;
  }

  ws.onopen = async () => {
    console.log("[social-fetch] Connected");
    setState("connected");
    reconnectAttempts = 0;
    const settings = await getSettings();
    ws.send(JSON.stringify({
      type: "hello",
      agent: "social-fetch-extension",
      version: "1.1.0",
      settings,
    }));
  };

  ws.onmessage = async (event) => {
    let msg;
    try {
      msg = JSON.parse(event.data);
    } catch (e) {
      console.error("[social-fetch] Bad JSON from server:", event.data);
      return;
    }
    await handleCommand(msg);
  };

  ws.onclose = (event) => {
    console.log(`[social-fetch] Disconnected (code=${event.code})`);
    setState("disconnected");
    ws = null;
    reconnectAttempts++;
  };

  ws.onerror = (err) => {
    console.error("[social-fetch] WebSocket error:", err);
  };
}

function safeSend(msg) {
  if (!ws || ws.readyState !== WebSocket.OPEN) return;
  // Tag every reply with the extension's manifest version so the
  // bridge daemon (and anyone tailing audit logs) can see exactly
  // which content+background-script combo produced the response.
  // Useful when debugging stale-cache reload issues — if the version
  // doesn't match what the user just bumped, the reload didn't take.
  if (typeof msg === "object" && msg !== null && !("extension_version" in msg)) {
    try {
      msg.extension_version = chrome.runtime.getManifest().version;
    } catch (_) {
      // getManifest() shouldn't fail in MV3, but if it does, send
      // anyway so we don't blackhole replies on a metadata hiccup.
    }
  }
  ws.send(typeof msg === "string" ? msg : JSON.stringify(msg));
}

// ---------------------------------------------------------------------------
// Tab group management
// ---------------------------------------------------------------------------

async function ensureTabGroup(seedTabId) {
  if (tabGroupId !== null) {
    try {
      await chrome.tabGroups.get(tabGroupId);
      return tabGroupId;
    } catch (e) {
      tabGroupId = null;
    }
  }
  const existing = await chrome.tabGroups.query({ title: TAB_GROUP_TITLE });
  if (existing.length > 0) {
    tabGroupId = existing[0].id;
    return tabGroupId;
  }
  tabGroupId = await chrome.tabs.group({ tabIds: [seedTabId] });
  await chrome.tabGroups.update(tabGroupId, {
    title: TAB_GROUP_TITLE,
    color: TAB_GROUP_COLOR,
    collapsed: true,
  });
  return tabGroupId;
}

async function addTabToGroup(tabId) {
  const tab = await chrome.tabs.get(tabId);
  if (tab.groupId === tabGroupId) return;
  await chrome.tabs.group({ tabIds: [tabId], groupId: tabGroupId });
}

// ---------------------------------------------------------------------------
// Tab management
// ---------------------------------------------------------------------------

function urlToPattern(url) {
  try {
    const u = new URL(url);
    return `${u.origin}/*`;
  } catch (e) {
    return null;
  }
}

async function getTab(targetUrl) {
  const pattern = urlToPattern(targetUrl);
  if (tabGroupId !== null && pattern) {
    const groupTabs = await chrome.tabs.query({ url: pattern, groupId: tabGroupId });
    if (groupTabs.length > 0) return groupTabs[0];
  }
  if (pattern) {
    const allTabs = await chrome.tabs.query({ url: pattern });
    if (allTabs.length > 0) {
      const tab = allTabs[0];
      try { await ensureTabGroup(tab.id); await addTabToGroup(tab.id); } catch (e) {}
      return tab;
    }
  }
  const tab = await chrome.tabs.create({ url: targetUrl, active: false });
  await waitForTabLoad(tab.id);
  try {
    await ensureTabGroup(tab.id);
    await addTabToGroup(tab.id);
  } catch (e) {
    console.warn("[social-fetch] Tab grouping failed (non-fatal):", e.message);
  }
  return tab;
}

function waitForTabLoad(tabId) {
  return new Promise((resolve) => {
    const listener = (id, info) => {
      if (id === tabId && info.status === "complete") {
        chrome.tabs.onUpdated.removeListener(listener);
        resolve();
      }
    };
    chrome.tabs.onUpdated.addListener(listener);
  });
}

// ---------------------------------------------------------------------------
// Content script injection
// ---------------------------------------------------------------------------

function getContentScripts(url) {
  const scripts = ["content.js"];
  if (url.includes("linkedin.com")) scripts.push("feeds/linkedin.js");
  else if (url.includes("x.com") || url.includes("twitter.com")) scripts.push("feeds/twitter.js");
  return scripts;
}

async function sendToContent(tab, action, params = {}) {
  try {
    return await chrome.tabs.sendMessage(tab.id, { action, ...params });
  } catch (err) {
    const scripts = getContentScripts(tab.url || "");
    await chrome.scripting.executeScript({ target: { tabId: tab.id }, files: scripts });
    return await chrome.tabs.sendMessage(tab.id, { action, ...params });
  }
}

// ---------------------------------------------------------------------------
// Command handlers
// ---------------------------------------------------------------------------

// hasHostPermission checks whether the extension is allowed to inject
// scripts into / read DOM from the given URL. Returns false for
// origins not covered by either the static host_permissions in the
// manifest or any optional permissions the user has granted via
// the popup. Used to fail fast with a clear message before we try
// to navigate a tab into a site we can't actually scrape.
async function hasHostPermission(url) {
  try {
    const u = new URL(url);
    const origin = `${u.protocol}//${u.host}/*`;
    return await chrome.permissions.contains({ origins: [origin] });
  } catch (e) {
    return false;
  }
}

async function handleCommand(msg) {
  const { id, command } = msg;
  console.log(`[social-fetch] Command: ${command} (id=${id})`);

  try {
    // Ping doesn't need a tab
    if (command === "ping") {
      safeSend({ id, command, status: "ok", pong: true });
      return;
    }

    const targetUrl = msg.url || "https://www.linkedin.com/feed/";

    // Fail fast if the user hasn't granted host permission for this
    // site — much friendlier than letting the navigate succeed and
    // then having executeScript fail with a cryptic error. Surfaces
    // as a "permission_required" status the bridge daemon can turn
    // into an actionable message for the agent / CLI user.
    if (!(await hasHostPermission(targetUrl))) {
      const host = (() => { try { return new URL(targetUrl).host; } catch { return targetUrl; } })();
      safeSend({
        id, command,
        status: "error",
        error: "permission_required",
        host,
        message: `bridge has no host permission for ${host} — open the extension popup and toggle "Allow on all sites" on, or add the host to the manifest's host_permissions.`,
      });
      return;
    }

    const tab = await getTab(targetUrl);
    let result;

    switch (command) {

      case "navigate": {
        const settings = await getSettings();
        const delay = settings.scrollDelay || 2000;
        await chrome.tabs.update(tab.id, { url: msg.url });
        await waitForTabLoad(tab.id);
        try { await addTabToGroup(tab.id); } catch (e) {}
        await sleep(delay);
        safeSend({ id, command, status: "ok", url: msg.url });
        return;
      }

      case "get_html":
        result = await sendToContent(tab, "get_html");
        safeSend({ id, command, status: "ok", html: result.html, url: result.url, title: result.title });
        return;

      case "screenshot": {
        const dataUrl = await chrome.tabs.captureVisibleTab(tab.windowId, { format: "png" });
        safeSend({ id, command, status: "ok", screenshot: dataUrl });
        return;
      }

      case "scroll": {
        result = await sendToContent(tab, "scroll", { amount: msg.amount || 1000 });
        // Forward every field the content script returned. Earlier
        // versions hard-coded just `scrollY` and silently dropped
        // diagnostic fields like `innerScrollTop` (set by the
        // multi-tier scroll handler when window.scrollBy doesn't
        // move the page — common on React SPAs like LinkedIn's
        // SDUI search). Any future content-script field
        // (`scroller`, `moved`, etc.) lands automatically.
        safeSend({ id, command, status: "ok", ...result });
        return;
      }

      case "enumerate_scrollers": {
        result = await sendToContent(tab, "enumerate_scrollers");
        safeSend({ id, command, status: "ok", ...result });
        return;
      }

      case "wheel": {
        result = await sendToContent(tab, "wheel", { deltaY: msg.deltaY || 1000 });
        safeSend({ id, command, status: "ok", ...result });
        return;
      }

      case "scroll_to_bottom": {
        result = await sendToContent(tab, "scroll_to_bottom");
        safeSend({ id, command, status: "ok", ...result });
        return;
      }

      default:
        safeSend({ id, command, status: "error", error: `Unknown command: ${command}` });
    }
  } catch (err) {
    console.error(`[social-fetch] Command error (${command}):`, err);
    safeSend({ id, command, status: "error", error: err.message || String(err) });
  }
}

function sleep(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

// ---------------------------------------------------------------------------
// Lifecycle
// ---------------------------------------------------------------------------

chrome.runtime.onMessage.addListener((msg, sender, sendResponse) => {
  if (msg.action === "getStatus") {
    sendResponse({ state: connectionState });
  } else if (msg.action === "connect") {
    connect();
    sendResponse({ ok: true });
  } else if (msg.action === "disconnect") {
    if (ws) ws.close();
    sendResponse({ ok: true });
  } else if (msg.action === "settingsUpdated") {
    if (ws && ws.readyState === WebSocket.OPEN) {
      ws.send(JSON.stringify({ type: "settings", settings: msg.settings }));
    }
    sendResponse({ ok: true });
  }
  return true;
});

// Content script keepalive ports — keeps the service worker alive
// as long as a matched page is open in the browser
chrome.runtime.onConnect.addListener((port) => {
  if (port.name === "keepalive") {
    console.log("[social-fetch] Keepalive port connected");
    port.onDisconnect.addListener(() => {
      console.log("[social-fetch] Keepalive port disconnected");
    });
  }
});

// Verify alarm exists on startup
chrome.alarms.get("checkConnection", (alarm) => {
  console.log("[social-fetch] checkConnection alarm:", alarm ? `exists, next at ${new Date(alarm.scheduledTime)}` : "MISSING — creating");
  if (!alarm) {
    chrome.alarms.create("checkConnection", { periodInMinutes: 0.1 });
  }
});

connect();
