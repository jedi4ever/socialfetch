// social-ui — vanilla JS, no framework, no build step.
//
// State model:
//   tabs[]             list of {id (uuid), sessionId, title, runId, prompt, lastSnapshot}
//   activeTabId        which tab the operator is looking at
//
// All persisted under localStorage["social-ui:v1"]. Each tab
// polls /api/sessions/<sid>/runs/<rid> on its own setInterval
// while its run is still running; many tabs poll in parallel.
//
// API call shape:
//   POST /api/sessions             → {session_id}
//   DELETE /api/sessions/<sid>     → {closed: <sid>}
//   POST /api/sessions/<sid>/runs  → {run_id}
//   GET  /api/sessions/<sid>/runs/<rid> → snapshot {status, events,
//          response, artifacts, exit_code, error, started_at,
//          finished_at, last_event_at, ...}

(() => {
  "use strict";

  const STORAGE_KEY = "social-ui:v1";
  const POLL_MS = 700;

  // ---- state -------------------------------------------------------

  const state = {
    tabs: [],         // [{id, sessionId, runId, prompt, title, lastSnapshot}]
    activeTabId: null,
  };

  function loadState() {
    try {
      const raw = localStorage.getItem(STORAGE_KEY);
      if (!raw) return;
      const parsed = JSON.parse(raw);
      if (Array.isArray(parsed.tabs)) state.tabs = parsed.tabs;
      if (typeof parsed.activeTabId === "string") state.activeTabId = parsed.activeTabId;
    } catch (e) {
      console.warn("could not parse stored state", e);
    }
  }
  function saveState() {
    localStorage.setItem(STORAGE_KEY, JSON.stringify({
      tabs: state.tabs,
      activeTabId: state.activeTabId,
    }));
  }

  function uuid() {
    if (crypto && crypto.randomUUID) return crypto.randomUUID();
    return "t-" + Math.random().toString(36).slice(2);
  }

  function findTab(id) { return state.tabs.find(t => t.id === id); }

  // ---- API ---------------------------------------------------------

  async function apiCreateSession() {
    const r = await fetch("/api/sessions", { method: "POST" });
    if (!r.ok) throw new Error("create session: " + (await r.text()));
    return r.json();   // {session_id}
  }
  async function apiCloseSession(sid) {
    await fetch("/api/sessions/" + encodeURIComponent(sid), { method: "DELETE" });
  }
  async function apiStartRun(sid, prompt) {
    const r = await fetch("/api/sessions/" + encodeURIComponent(sid) + "/runs", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ prompt }),
    });
    if (!r.ok) throw new Error("start run: " + (await r.text()));
    return r.json();   // {run_id}
  }
  async function apiRunStatus(sid, rid) {
    const r = await fetch("/api/sessions/" + encodeURIComponent(sid) + "/runs/" + encodeURIComponent(rid));
    if (!r.ok) throw new Error("run_status: " + (await r.text()));
    return r.json();
  }

  // ---- rendering ---------------------------------------------------

  const tabsEl = document.getElementById("tabs");
  const newBtn = document.getElementById("newTabBtn");
  const panes = document.getElementById("panes");
  const paneTpl = document.getElementById("pane-template");

  function tabTitle(t) {
    if (t.prompt) {
      const s = t.prompt.trim();
      return s.length > 32 ? s.slice(0, 29) + "…" : s;
    }
    return "untitled";
  }

  function renderTabs() {
    // Remove all children except the "+ New" button.
    [...tabsEl.querySelectorAll(".tab:not(.newTab)")].forEach(el => el.remove());
    state.tabs.forEach(tab => {
      const btn = document.createElement("button");
      btn.className = "tab" + (tab.id === state.activeTabId ? " active" : "");
      btn.type = "button";
      btn.dataset.tabId = tab.id;
      btn.innerHTML = `<span class="title"></span><span class="close" title="Close tab">×</span>`;
      btn.querySelector(".title").textContent = tabTitle(tab);
      btn.addEventListener("click", e => {
        if (e.target.classList.contains("close")) {
          e.stopPropagation();
          closeTab(tab.id);
        } else {
          activateTab(tab.id);
        }
      });
      tabsEl.insertBefore(btn, newBtn);
    });
  }

  function ensurePane(tab) {
    let pane = panes.querySelector(`[data-tab-id="${tab.id}"]`);
    if (pane) return pane;
    const node = paneTpl.content.firstElementChild.cloneNode(true);
    node.dataset.tabId = tab.id;
    node.querySelector(".prompt-form").addEventListener("submit", e => {
      e.preventDefault();
      sendPrompt(tab.id);
    });
    node.querySelector(".prompt-input").addEventListener("keydown", e => {
      if ((e.metaKey || e.ctrlKey) && e.key === "Enter") {
        e.preventDefault();
        sendPrompt(tab.id);
      }
    });
    // Events-sidebar collapse toggle. State is global (one
    // preference for all tabs) so flipping it once sticks for
    // every tab in this browser.
    const sidebar = node.querySelector(".events-sidebar");
    const toggle = node.querySelector(".toggle-events");
    sidebar.dataset.collapsed = (localStorage.getItem("social-ui:events-collapsed") === "true") ? "true" : "false";
    toggle.title = sidebar.dataset.collapsed === "true" ? "Show events" : "Hide events";
    toggle.addEventListener("click", () => {
      const next = sidebar.dataset.collapsed === "true" ? "false" : "true";
      // Apply to every existing pane's sidebar so the choice is
      // global-feeling.
      panes.querySelectorAll(".events-sidebar").forEach(s => {
        s.dataset.collapsed = next;
      });
      panes.querySelectorAll(".toggle-events").forEach(b => {
        b.title = next === "true" ? "Show events" : "Hide events";
      });
      localStorage.setItem("social-ui:events-collapsed", next);
    });
    panes.appendChild(node);
    return node;
  }

  function renderPane(tab) {
    const pane = ensurePane(tab);
    pane.hidden = (tab.id !== state.activeTabId);

    const status = pane.querySelector(".status-line");
    const events = pane.querySelector(".events-list");
    const response = pane.querySelector(".response-text");
    const artifacts = pane.querySelector(".artifacts-list");
    const promptInput = pane.querySelector(".prompt-input");
    const sendBtn = pane.querySelector(".send");

    // Status line.
    const snap = tab.lastSnapshot;
    if (!snap) {
      status.className = "status-line";
      status.textContent = tab.sessionId
        ? "session " + short(tab.sessionId) + " — waiting for prompt"
        : "no session yet — type a prompt and Send";
    } else if (snap.status === "running") {
      status.className = "status-line";
      const dur = formatDur(snap.started_at, new Date().toISOString());
      status.textContent = `running (${snap.events_count || 0} events, ${dur})`;
      sendBtn.disabled = true;
    } else if (snap.status === "done") {
      status.className = "status-line";
      const dur = snap.duration_seconds ? Math.round(snap.duration_seconds) + "s" : "";
      status.textContent = `done (${dur}, exit ${snap.exit_code ?? 0})`;
      sendBtn.disabled = false;
    } else if (snap.status === "error") {
      status.className = "status-line error";
      status.textContent = "error: " + (snap.error || "(unknown)");
      sendBtn.disabled = false;
    }
    if (tab.prompt && !tab.lastSnapshot) {
      promptInput.value = tab.prompt;
    }

    // Events list. The server returns the runRecord.Events
    // []json.RawMessage as already-decoded JSON objects (raw
    // messages get re-emitted as JSON, not as quoted strings),
    // so the client just consumes them. We still tolerate strings
    // for forward-compat in case the wire shape changes.
    events.innerHTML = "";
    if (snap && Array.isArray(snap.events)) {
      snap.events.forEach(ev => {
        let parsed = null;
        if (ev && typeof ev === "object") {
          parsed = ev;
        } else if (typeof ev === "string") {
          try { parsed = JSON.parse(ev); } catch { /* leave null */ }
        }
        const li = document.createElement("li");
        const t = parsed?.type || "raw";
        li.className = t;
        li.textContent = renderEventLine(parsed) || "(unparseable event)";
        events.appendChild(li);
      });
    }

    // Response.
    response.textContent = (snap && snap.response) || "";

    // Artifacts.
    artifacts.innerHTML = "";
    if (snap && Array.isArray(snap.artifacts)) {
      snap.artifacts.forEach(url => {
        const li = document.createElement("li");
        const a = document.createElement("a");
        a.href = url;
        a.target = "_blank";
        a.rel = "noopener";
        a.textContent = url.split("/").pop().split("?")[0];
        li.appendChild(a);
        artifacts.appendChild(li);
      });
    }
  }

  function renderEventLine(p) {
    if (!p) return "";
    switch (p.type) {
      case "system":
        return `▸ system/${p.subtype || "?"}  model=${p.model || "?"}`;
      case "user":
        return `▸ user`;
      case "assistant": {
        const txt = (p.message?.content || [])
          .filter(c => c.type === "text").map(c => c.text).join("").trim();
        const tools = (p.message?.content || [])
          .filter(c => c.type === "tool_use").map(c => c.name).join(", ");
        if (tools) return `▸ tool_use: ${tools}`;
        if (txt) return `▸ assistant: ${truncate(txt, 160)}`;
        return `▸ assistant`;
      }
      case "result":
        return `▸ result: ${p.subtype} stop_reason=${p.stop_reason || "?"} duration=${p.duration_ms || 0}ms cost=$${(p.total_cost_usd || 0).toFixed(4)}`;
      default:
        return `▸ ${p.type}`;
    }
  }

  function truncate(s, n) {
    s = s.replace(/\s+/g, " ");
    return s.length > n ? s.slice(0, n - 1) + "…" : s;
  }
  function short(s) { return s ? s.slice(0, 12) : ""; }

  function formatDur(startISO, endISO) {
    if (!startISO) return "";
    const s = new Date(startISO).getTime();
    const e = new Date(endISO).getTime();
    const ms = Math.max(0, e - s);
    if (ms < 60_000) return Math.round(ms / 1000) + "s";
    const m = Math.floor(ms / 60_000);
    return m + "m " + Math.round((ms % 60_000) / 1000) + "s";
  }

  // ---- actions -----------------------------------------------------

  function activateTab(id) {
    state.activeTabId = id;
    saveState();
    renderTabs();
    state.tabs.forEach(t => renderPane(t));
  }

  async function newTab() {
    const tab = { id: uuid(), sessionId: null, runId: null, prompt: "", title: "", lastSnapshot: null };
    state.tabs.push(tab);
    state.activeTabId = tab.id;
    saveState();
    renderTabs();
    renderPane(tab);
    // We DON'T create the session yet — wait until the operator
    // hits Send. Keeps "untitled" tabs cheap.
  }

  async function closeTab(id) {
    const tab = findTab(id);
    if (!tab) return;
    state.tabs = state.tabs.filter(t => t.id !== id);
    if (state.activeTabId === id) {
      state.activeTabId = state.tabs.length ? state.tabs[state.tabs.length - 1].id : null;
    }
    saveState();
    if (tab.sessionId) {
      try { await apiCloseSession(tab.sessionId); } catch { /* best-effort */ }
    }
    const pane = panes.querySelector(`[data-tab-id="${id}"]`);
    if (pane) pane.remove();
    renderTabs();
    state.tabs.forEach(t => renderPane(t));
  }

  async function sendPrompt(tabId) {
    const tab = findTab(tabId);
    if (!tab) return;
    const pane = ensurePane(tab);
    const input = pane.querySelector(".prompt-input");
    const prompt = input.value.trim();
    if (!prompt) return;
    tab.prompt = prompt;
    saveState();

    try {
      if (!tab.sessionId) {
        const s = await apiCreateSession();
        tab.sessionId = s.session_id;
      }
      const r = await apiStartRun(tab.sessionId, prompt);
      tab.runId = r.run_id;
      tab.lastSnapshot = { status: "running", started_at: new Date().toISOString(), events: [], events_count: 0 };
      saveState();
      renderTabs();
      renderPane(tab);
      pollUntilDone(tab.id);
    } catch (e) {
      tab.lastSnapshot = { status: "error", error: e.message };
      saveState();
      renderPane(tab);
    }
  }

  const polling = new Map();

  async function pollUntilDone(tabId) {
    if (polling.has(tabId)) clearInterval(polling.get(tabId));
    const tick = async () => {
      const tab = findTab(tabId);
      if (!tab || !tab.sessionId || !tab.runId) {
        clearInterval(polling.get(tabId));
        polling.delete(tabId);
        return;
      }
      try {
        const snap = await apiRunStatus(tab.sessionId, tab.runId);
        tab.lastSnapshot = snap;
        saveState();
        renderPane(tab);
        if (snap.status !== "running") {
          clearInterval(polling.get(tabId));
          polling.delete(tabId);
        }
      } catch (e) {
        // Soft failure — keep polling. A transient 502 (host MCP
        // restart) shouldn't kill the loop.
        console.warn("poll error", e);
      }
    };
    tick();
    const id = setInterval(tick, POLL_MS);
    polling.set(tabId, id);
  }

  function rehydrate() {
    state.tabs.forEach(tab => {
      renderPane(tab);
      if (tab.runId && tab.lastSnapshot && tab.lastSnapshot.status === "running") {
        pollUntilDone(tab.id);
      }
    });
  }

  // ---- bootstrap ---------------------------------------------------

  newBtn.addEventListener("click", newTab);
  loadState();
  if (state.tabs.length === 0) {
    newTab();
  } else {
    if (!state.activeTabId || !findTab(state.activeTabId)) {
      state.activeTabId = state.tabs[0].id;
    }
    renderTabs();
    rehydrate();
  }
})();
