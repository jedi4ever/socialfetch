/**
 * Offscreen document — maintains the WebSocket connection to social-fetch server.
 *
 * Chrome MV3 kills service workers after 30s of inactivity, but offscreen
 * documents persist as long as they're alive. We move the WebSocket here
 * and relay messages to/from the background via chrome.runtime messaging.
 */

const DEFAULT_SERVER = "ws://127.0.0.1:5555/ws/extension";
const RECONNECT_DELAYS = [1000, 2000, 4000, 8000, 16000, 30000];

let ws = null;
let reconnectAttempt = 0;

async function getServerUrl() {
  return new Promise((resolve) => {
    chrome.storage.local.get(["serverUrl"], (result) => {
      resolve(result.serverUrl || DEFAULT_SERVER);
    });
  });
}

async function getSettings() {
  return new Promise((resolve) => {
    chrome.storage.local.get(["settings"], (result) => {
      resolve(result.settings || {});
    });
  });
}

async function connect() {
  if (ws && ws.readyState <= WebSocket.OPEN) return;

  const serverUrl = await getServerUrl();
  console.log(`[social-fetch offscreen] Connecting to ${serverUrl}...`);

  try {
    ws = new WebSocket(serverUrl);
  } catch (err) {
    console.error("[social-fetch offscreen] WebSocket error:", err);
    scheduleReconnect();
    return;
  }

  ws.onopen = async () => {
    console.log("[social-fetch offscreen] Connected");
    reconnectAttempt = 0;
    chrome.runtime.sendMessage({ type: "ws_state", state: "connected" });
    const settings = await getSettings();
    ws.send(JSON.stringify({
      type: "hello",
      agent: "social-fetch-extension",
      version: "1.1.0",
      settings,
    }));
  };

  ws.onmessage = (event) => {
    // Forward server messages to background script
    try {
      const msg = JSON.parse(event.data);
      chrome.runtime.sendMessage({ type: "ws_message", payload: msg });
    } catch (e) {
      console.error("[social-fetch offscreen] Bad JSON:", event.data);
    }
  };

  ws.onclose = () => {
    console.log("[social-fetch offscreen] Disconnected");
    ws = null;
    chrome.runtime.sendMessage({ type: "ws_state", state: "disconnected" });
    scheduleReconnect();
  };

  ws.onerror = (err) => {
    console.error("[social-fetch offscreen] WS error:", err);
  };
}

function scheduleReconnect() {
  const delay = RECONNECT_DELAYS[Math.min(reconnectAttempt, RECONNECT_DELAYS.length - 1)];
  reconnectAttempt++;
  setTimeout(connect, delay);
}

// Listen for messages from background script
chrome.runtime.onMessage.addListener((msg, sender, sendResponse) => {
  if (msg.type === "ws_send") {
    // Background wants to send a message through the WS
    if (ws && ws.readyState === WebSocket.OPEN) {
      ws.send(JSON.stringify(msg.payload));
      sendResponse({ ok: true });
    } else {
      sendResponse({ ok: false, error: "WebSocket not connected" });
    }
  } else if (msg.type === "ws_connect") {
    connect();
    sendResponse({ ok: true });
  } else if (msg.type === "ws_settings") {
    if (ws && ws.readyState === WebSocket.OPEN) {
      ws.send(JSON.stringify({ type: "settings", settings: msg.settings }));
    }
    sendResponse({ ok: true });
  }
  return true;
});

// Connect immediately
connect();
