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

  function activate(tab) {
    buttons.forEach(function (b) {
      b.classList.toggle("active", b.dataset.tab === tab);
    });
    Object.keys(panels).forEach(function (key) {
      if (panels[key]) panels[key].style.display = key === tab ? "block" : "none";
    });

    lazySrc(panels[tab]); // container tab uses an iframe like the terminal did

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

  // Activate the initially-selected tab on load.
  var initial = document.querySelector(".tab-btn.active") || buttons[0];
  if (initial) activate(initial.dataset.tab);
})();
