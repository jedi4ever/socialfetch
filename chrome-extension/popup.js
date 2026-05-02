/**
 * Popup script — handles status display, connection controls, and settings.
 */

const DEFAULT_SERVER = "ws://127.0.0.1:5555/ws/extension";

const DEFAULT_SETTINGS = {
  scrollDelay: 2000,
  maxPosts: 20,
  linkedinEnabled: true,
  linkedinScrollCount: 3,
  twitterEnabled: true,
  twitterScrollCount: 3,
};

// ---------------------------------------------------------------------------
// Tab switching
// ---------------------------------------------------------------------------

document.querySelectorAll(".tab").forEach((tab) => {
  tab.addEventListener("click", () => {
    document.querySelectorAll(".tab").forEach((t) => t.classList.remove("active"));
    document.querySelectorAll(".panel").forEach((p) => p.classList.remove("active"));
    tab.classList.add("active");
    document.getElementById(`panel-${tab.dataset.tab}`).classList.add("active");
  });
});

// ---------------------------------------------------------------------------
// Status panel
// ---------------------------------------------------------------------------

const dot = document.getElementById("dot");
const statusText = document.getElementById("statusText");
const serverUrlInput = document.getElementById("serverUrl");
const connectBtn = document.getElementById("connectBtn");
const disconnectBtn = document.getElementById("disconnectBtn");

function updateStatus(state) {
  dot.className = "dot " + state;
  const labels = {
    connected: "Connected",
    connecting: "Connecting...",
    disconnected: "Disconnected",
  };
  statusText.textContent = labels[state] || state;
}

// Load state
chrome.storage.local.get(["serverUrl", "connectionState"], (result) => {
  serverUrlInput.value = result.serverUrl || DEFAULT_SERVER;
  updateStatus(result.connectionState || "disconnected");
});

chrome.storage.onChanged.addListener((changes) => {
  if (changes.connectionState) {
    updateStatus(changes.connectionState.newValue);
  }
});

connectBtn.addEventListener("click", () => {
  const url = serverUrlInput.value.trim() || DEFAULT_SERVER;
  chrome.storage.local.set({ serverUrl: url }, () => {
    chrome.runtime.sendMessage({ action: "connect" });
  });
});

disconnectBtn.addEventListener("click", () => {
  chrome.runtime.sendMessage({ action: "disconnect" });
});

serverUrlInput.addEventListener("change", () => {
  const url = serverUrlInput.value.trim() || DEFAULT_SERVER;
  chrome.storage.local.set({ serverUrl: url });
});

// ---------------------------------------------------------------------------
// Settings panel
// ---------------------------------------------------------------------------

const settingsFields = {
  scrollDelay: document.getElementById("scrollDelay"),
  maxPosts: document.getElementById("maxPosts"),
  linkedinEnabled: document.getElementById("linkedinEnabled"),
  linkedinScrollCount: document.getElementById("linkedinScrollCount"),
  twitterEnabled: document.getElementById("twitterEnabled"),
  twitterScrollCount: document.getElementById("twitterScrollCount"),
};

const saveBtn = document.getElementById("saveSettingsBtn");
const resetBtn = document.getElementById("resetSettingsBtn");
const savedMsg = document.getElementById("savedMsg");

// Load settings
chrome.storage.local.get(["settings"], (result) => {
  const settings = { ...DEFAULT_SETTINGS, ...(result.settings || {}) };
  applySettingsToUI(settings);
});

function applySettingsToUI(settings) {
  settingsFields.scrollDelay.value = settings.scrollDelay;
  settingsFields.maxPosts.value = settings.maxPosts;
  settingsFields.linkedinEnabled.checked = settings.linkedinEnabled;
  settingsFields.linkedinScrollCount.value = settings.linkedinScrollCount;
  settingsFields.twitterEnabled.checked = settings.twitterEnabled;
  settingsFields.twitterScrollCount.value = settings.twitterScrollCount;
}

function readSettingsFromUI() {
  return {
    scrollDelay: parseInt(settingsFields.scrollDelay.value, 10) || DEFAULT_SETTINGS.scrollDelay,
    maxPosts: parseInt(settingsFields.maxPosts.value, 10) || DEFAULT_SETTINGS.maxPosts,
    linkedinEnabled: settingsFields.linkedinEnabled.checked,
    linkedinScrollCount: parseInt(settingsFields.linkedinScrollCount.value, 10) || DEFAULT_SETTINGS.linkedinScrollCount,
    twitterEnabled: settingsFields.twitterEnabled.checked,
    twitterScrollCount: parseInt(settingsFields.twitterScrollCount.value, 10) || DEFAULT_SETTINGS.twitterScrollCount,
  };
}

saveBtn.addEventListener("click", () => {
  const settings = readSettingsFromUI();
  chrome.storage.local.set({ settings }, () => {
    // Notify background to forward to server
    chrome.runtime.sendMessage({ action: "settingsUpdated", settings });
    savedMsg.style.display = "block";
    setTimeout(() => { savedMsg.style.display = "none"; }, 2000);
  });
});

resetBtn.addEventListener("click", () => {
  applySettingsToUI(DEFAULT_SETTINGS);
  chrome.storage.local.set({ settings: DEFAULT_SETTINGS }, () => {
    chrome.runtime.sendMessage({ action: "settingsUpdated", settings: DEFAULT_SETTINGS });
    savedMsg.style.display = "block";
    setTimeout(() => { savedMsg.style.display = "none"; }, 2000);
  });
});
