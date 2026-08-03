(function () {
  var projectId = document.body.dataset.projectId;
  if (!projectId) return; // landing page, nothing to wire up

  var buttons = document.querySelectorAll(".tab-btn");
  var panels = {
    files: document.getElementById("tab-files"),
    diff: document.getElementById("tab-diff"),
    container: document.getElementById("tab-container"),
    mitm: document.getElementById("tab-mitm"),
    firewall: document.getElementById("tab-firewall"),
    config: document.getElementById("tab-config"),
  };
  var started = {}; // tab -> true once its lazy init has run

  // Lazily assign a screen iframe's real src on first activation so no PTY (or
  // other backend work) starts until the tab is opened.
  function lazySrc(panel) {
    var iframe = panel && panel.querySelector("iframe.screen-iframe");
    if (iframe && !iframe.getAttribute("src") && iframe.dataset.src) {
      iframe.setAttribute("src", iframe.dataset.src);
    }
  }

  // The Container tab's iframe reflects container up/down at ITS load time. If it
  // loaded while the container was down (showing "not running"), re-point its src
  // when the tab is reopened so it picks up a container that has since started.
  // A LIVE shell iframe (session up) is left alone so we don't kill the session.
  function refreshContainerIfDown() {
    var panel = panels.container;
    var iframe = panel && panel.querySelector("iframe.screen-iframe");
    if (!iframe || !iframe.dataset.src) return;
    if (!iframe.getAttribute("src")) { lazySrc(panel); return; } // first open
    var down = true;
    try {
      var doc = iframe.contentDocument;
      // container.html.tmpl sets data-session-up on <body>; "true" => live shell.
      down = !doc || doc.body.getAttribute("data-session-up") !== "true";
    } catch (e) { down = false; } // cross-origin shouldn't happen; be conservative
    if (down) iframe.setAttribute("src", iframe.dataset.src + "?t=" + Date.now());
  }

  function activate(tab) {
    buttons.forEach(function (b) {
      b.classList.toggle("active", b.dataset.tab === tab);
    });
    Object.keys(panels).forEach(function (key) {
      if (panels[key]) panels[key].style.display = key === tab ? "block" : "none";
    });

    if (tab === "container") refreshContainerIfDown();
    else lazySrc(panels[tab]); // other screen tabs: first-open lazy src

    if (started[tab]) return;
    if (tab === "files" && typeof startFiles === "function") { started.files = true; startFiles(projectId); }
    else if (tab === "diff" && typeof startDiff === "function") { started.diff = true; startDiff(projectId); }
    else if (tab === "firewall") { started.firewall = true; startFirewallStream(); }
    else if (tab === "mitm" && typeof startMitmFlows === "function") { started.mitm = true; startMitmFlows(projectId); }
    else if (tab === "config" && typeof startConfig === "function") { started.config = true; startConfig(projectId); }
  }

  function startFirewallStream() {
    var logEl = document.getElementById("firewall-log");
    if (!logEl) return;
    var es = new EventSource("/p/" + projectId + "/firewall/stream");
    es.onmessage = function (e) {
      logEl.textContent += e.data + "\n";
      logEl.scrollTop = logEl.scrollHeight;
    };
    es.addEventListener("error", function () {
      logEl.textContent += "[stream disconnected]\n";
    });
  }

  buttons.forEach(function (b) {
    b.addEventListener("click", function () { activate(b.dataset.tab); });
  });

  // ---- Detachable Claude terminal dock --------------------------------------
  // Collapse state persists in localStorage (a single UI pref, not per-project).
  var DOCK_KEY = "sandclaude.dockCollapsed";
  var layout = document.getElementById("project-layout");
  var dock = document.getElementById("term-dock");
  var dockToggle = document.getElementById("dock-toggle");

  function applyDock(collapsed) {
    if (!layout || !dock) return;
    layout.classList.toggle("dock-collapsed", collapsed);
    if (dockToggle) dockToggle.classList.toggle("on", !collapsed);
    if (!collapsed) lazySrc(dock); // spawn the terminal PTY only when first shown
  }
  if (dockToggle) {
    dockToggle.addEventListener("click", function () {
      var collapsed = !layout.classList.contains("dock-collapsed");
      // toggling: if currently NOT collapsed -> collapse; else expand
      collapsed = layout.classList.contains("dock-collapsed") ? false : true;
      try { localStorage.setItem(DOCK_KEY, collapsed ? "1" : "0"); } catch (e) {}
      applyDock(collapsed);
    });
  }
  // Initial dock state: expanded by default (so the terminal is visible), unless
  // the user previously collapsed it.
  var startCollapsed = false;
  try { startCollapsed = localStorage.getItem(DOCK_KEY) === "1"; } catch (e) {}
  applyDock(startCollapsed);

  // ---- Host-terminal overlay (VS Code integrated terminal) ------------------
  var overlay = document.getElementById("host-overlay");
  var hostToggle = document.getElementById("host-toggle");
  var hostClose = document.getElementById("host-close");
  var hostHandle = document.getElementById("host-overlay-handle");
  var HOST_H_KEY = "sandclaude.hostOverlayHeight";

  function hostVisible() { return overlay && !overlay.hasAttribute("hidden"); }
  function showHost(show) {
    if (!overlay) return;
    if (show) {
      var h = 0;
      try { h = parseInt(localStorage.getItem(HOST_H_KEY) || "0", 10); } catch (e) {}
      if (h > 80) overlay.style.height = h + "px";
      overlay.removeAttribute("hidden");
      // Spawn the host shell only on first open (lazy iframe src).
      var f = document.getElementById("host-iframe");
      if (f && !f.getAttribute("src") && f.dataset.src) f.setAttribute("src", f.dataset.src);
    } else {
      overlay.setAttribute("hidden", "");
    }
    if (hostToggle) hostToggle.classList.toggle("on", show);
  }

  if (hostToggle) hostToggle.addEventListener("click", function () { showHost(!hostVisible()); });
  if (hostClose) hostClose.addEventListener("click", function () { showHost(false); });
  // (host-terminal toggle is registered in the global hotkey layer near the end.)

  // Drag the top handle to resize.
  if (hostHandle && overlay) {
    hostHandle.addEventListener("mousedown", function (start) {
      start.preventDefault();
      var startY = start.clientY, startH = overlay.offsetHeight;
      function onMove(ev) {
        var h = Math.max(120, Math.min(window.innerHeight - 120, startH + (startY - ev.clientY)));
        overlay.style.height = h + "px";
      }
      function onUp() {
        document.removeEventListener("mousemove", onMove);
        document.removeEventListener("mouseup", onUp);
        try { localStorage.setItem(HOST_H_KEY, String(overlay.offsetHeight)); } catch (e) {}
      }
      document.addEventListener("mousemove", onMove);
      document.addEventListener("mouseup", onUp);
    });
  }

  // ---- "Ask Claude" chat panel (left/right dockable slide-out) --------------
  var chat = document.getElementById("chat-panel");
  var chatToggle = document.getElementById("chat-toggle");
  var chatClose = document.getElementById("chat-close");
  var chatDock = document.getElementById("chat-dock");
  var chatHandle = document.getElementById("chat-panel-handle");
  var CHAT_W_KEY = "sandclaude.chatWidth";
  var CHAT_DOCK_KEY = "sandclaude.chatDock"; // "left" | "right"

  function chatVisible() { return chat && !chat.hasAttribute("hidden"); }

  function applyChatDock() {
    if (!chat) return;
    var side = "right";
    try { side = localStorage.getItem(CHAT_DOCK_KEY) || "right"; } catch (e) {}
    chat.classList.toggle("dock-left", side === "left");
  }

  function showChat(show) {
    if (!chat) return;
    if (show) {
      var wpx = 0;
      try { wpx = parseInt(localStorage.getItem(CHAT_W_KEY) || "0", 10); } catch (e) {}
      if (wpx > 240) chat.style.width = wpx + "px";
      applyChatDock();
      chat.removeAttribute("hidden");
      // Spawn the host claude only on first open (lazy iframe src).
      var f = document.getElementById("chat-iframe");
      if (f && !f.getAttribute("src") && f.dataset.src) f.setAttribute("src", f.dataset.src);
    } else {
      chat.setAttribute("hidden", "");
    }
    if (chatToggle) chatToggle.classList.toggle("on", show);
  }

  if (chatToggle) chatToggle.addEventListener("click", function () { showChat(!chatVisible()); });
  if (chatClose) chatClose.addEventListener("click", function () { showChat(false); });
  if (chatDock) chatDock.addEventListener("click", function () {
    var side = "right";
    try { side = localStorage.getItem(CHAT_DOCK_KEY) || "right"; } catch (e) {}
    side = side === "left" ? "right" : "left";
    try { localStorage.setItem(CHAT_DOCK_KEY, side); } catch (e) {}
    applyChatDock();
  });
  // (Ask-Claude toggle is registered in the global hotkey layer near the end.)

  // Drag the inner-edge handle to resize the panel width.
  if (chatHandle && chat) {
    chatHandle.addEventListener("mousedown", function (start) {
      start.preventDefault();
      var startX = start.clientX, startW = chat.offsetWidth;
      var dockedLeft = chat.classList.contains("dock-left");
      function onMove(ev) {
        // When docked right, dragging left (smaller X) widens; mirror when left.
        var delta = dockedLeft ? (ev.clientX - startX) : (startX - ev.clientX);
        var wpx = Math.max(280, Math.min(window.innerWidth - 120, startW + delta));
        chat.style.width = wpx + "px";
      }
      function onUp() {
        document.removeEventListener("mousemove", onMove);
        document.removeEventListener("mouseup", onUp);
        try { localStorage.setItem(CHAT_W_KEY, String(chat.offsetWidth)); } catch (e) {}
      }
      document.addEventListener("mousemove", onMove);
      document.addEventListener("mouseup", onUp);
    });
  }

  // ---- Global hotkeys -------------------------------------------------------
  // A small keyboard layer for the project page. Existing per-panel toggles
  // (host shell, Ask Claude, etc.) are folded in here so they share one guard
  // and don't fire while you're typing into a field or the terminal.
  function toggleClaudeDock() {
    var collapsed = layout && layout.classList.contains("dock-collapsed");
    var next = !collapsed; // collapsed -> expand, expanded -> collapse
    try { localStorage.setItem(DOCK_KEY, next ? "1" : "0"); } catch (e) {}
    applyDock(next);
  }

  // Don't hijack keys while the user is typing (inputs, textareas, the xterm
  // canvas, or anything contenteditable). Panel iframes capture their own keys.
  function typingTarget() {
    var el = document.activeElement;
    if (!el) return false;
    var tag = (el.tagName || "").toLowerCase();
    return tag === "input" || tag === "textarea" || el.isContentEditable ||
      el.classList.contains("xterm-helper-textarea") || tag === "iframe";
  }

  // Cmd on macOS, Ctrl elsewhere — the combo label adapts; both modifiers are
  // accepted so the same binding works cross-platform.
  var isMac = /mac/i.test(navigator.platform || navigator.userAgent || "");
  var MOD = isMac ? "Cmd" : "Ctrl";
  var HOTKEYS = [
    { combo: MOD + "-J", key: "j", label: "Toggle host terminal", run: function () { showHost(!hostVisible()); } },
    { combo: MOD + "-K", key: "k", label: "Toggle Ask Claude", run: function () { showChat(!chatVisible()); } },
    { combo: MOD + "-;", key: ";", label: "Toggle Claude terminal", run: toggleClaudeDock },
    { combo: MOD + "-.", key: ".", label: "Clear notifications", run: function () {
        // Cross-project toasts (a later feature) listen for this event.
        document.dispatchEvent(new CustomEvent("sandclaude:clear-notifications"));
      } },
    { combo: MOD + "-/", key: "/", label: "Show shortcuts", run: showHotkeyHelp },
  ];

  document.addEventListener("keydown", function (e) {
    // Accept Cmd (metaKey) OR Ctrl, but not both other modifiers.
    if (!(e.metaKey || e.ctrlKey) || e.altKey) return;
    if (typingTarget()) return;
    var k = (e.key || "").toLowerCase();
    for (var i = 0; i < HOTKEYS.length; i++) {
      if (HOTKEYS[i].key === k) { e.preventDefault(); HOTKEYS[i].run(); return; }
    }
  });

  // A dismissible overlay listing the shortcuts.
  function showHotkeyHelp() {
    var existing = document.getElementById("hotkey-help");
    if (existing) { existing.remove(); return; } // toggle off
    var ov = document.createElement("div");
    ov.id = "hotkey-help";
    ov.className = "hotkey-help";
    var rows = HOTKEYS.map(function (h) {
      return '<div class="hk-row"><kbd>' + h.combo + "</kbd><span>" + h.label + "</span></div>";
    }).join("");
    ov.innerHTML = '<div class="hk-card"><div class="hk-title">Keyboard shortcuts</div>' + rows +
      '<div class="hk-hint">Esc or Ctrl-/ to close</div></div>';
    ov.addEventListener("click", function () { ov.remove(); });
    document.addEventListener("keydown", function esc(ev) {
      if (ev.key === "Escape") { ov.remove(); document.removeEventListener("keydown", esc); }
    });
    document.body.appendChild(ov);
  }

  // Activate the initially-selected tab on load.
  var initial = document.querySelector(".tab-btn.active") || buttons[0];
  if (initial) activate(initial.dataset.tab);

  // ---- Container power toggle (▶ Start / ■ Stop) in the header ---------------
  var powerBtn = document.getElementById("power-toggle");
  if (powerBtn) {
    var powerUp = powerBtn.dataset.up === "true";

    function paintPower() {
      powerBtn.textContent = powerUp ? "■ Stop" : "▶ Start";
      powerBtn.classList.toggle("is-up", powerUp);
    }
    paintPower();

    function doPower() {
      var stopping = powerUp;
      powerBtn.disabled = true;
      powerBtn.textContent = stopping ? "stopping…" : "starting…";
      fetch("/p/" + projectId + "/" + (stopping ? "stop" : "start"),
            { method: "POST", credentials: "same-origin" })
        .then(function (r) { return r.json().catch(function () { return {}; }).then(function (b) { return { status: r.status, body: b }; }); })
        .then(function (res) {
          // Start may 409 when SSH keys aren't loaded — load them inline via the
          // shared host-shell modal, then start.
          if (!stopping && res.status === 409 && res.body && res.body.ssh_keys_pending) {
            powerBtn.disabled = false; paintPower();
            if (typeof window.openSSHLoadModal === "function") {
              window.openSSHLoadModal(projectId, function (loaded) {
                if (loaded) doPowerStart();
              });
            }
            return;
          }
          if (res.status >= 400) throw new Error((res.body && res.body.message) || ("HTTP " + res.status));
          // Optimistically flip; the poll will correct if it didn't take.
          powerUp = !stopping;
          setTimeout(function () { powerBtn.disabled = false; paintPower(); }, 800);
        })
        .catch(function (err) {
          powerBtn.disabled = false; paintPower();
          alert((stopping ? "stop" : "start") + " failed: " + err.message);
        });
    }
    // Direct start (keys already loaded) — used after the inline load modal.
    function doPowerStart() {
      fetch("/p/" + projectId + "/start", { method: "POST", credentials: "same-origin" })
        .then(function () { powerUp = true; setTimeout(paintPower, 800); })
        .catch(function () {});
    }

    powerBtn.addEventListener("click", doPower);

    // Keep the label in sync with reality (someone may start/stop elsewhere, or
    // the container may exit). Light poll of /status.
    setInterval(function () {
      fetch("/status", { credentials: "same-origin" })
        .then(function (r) { return r.ok ? r.json() : null; })
        .then(function (s) {
          if (!s || !s.projects) return;
          var me = s.projects.filter(function (p) { return p.id === projectId; })[0];
          if (me && !powerBtn.disabled) { powerUp = !!me.container_up; paintPower(); }
        })
        .catch(function () {});
    }, 4000);
  }
})();
