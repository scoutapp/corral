(function () {
  var projectId = document.body.dataset.projectId;
  if (!projectId) return; // landing page, nothing to wire up

  var buttons = document.querySelectorAll(".tab-btn");
  var panels = {
    terminal: document.getElementById("tab-terminal"),
    mitm: document.getElementById("tab-mitm"),
    firewall: document.getElementById("tab-firewall"),
    config: document.getElementById("tab-config"),
  };
  var firewallStarted = false;
  var mitmStarted = false;
  var configStarted = false;

  function activate(tab) {
    buttons.forEach(function (b) {
      b.classList.toggle("active", b.dataset.tab === tab);
    });
    Object.keys(panels).forEach(function (key) {
      panels[key].style.display = key === tab ? "block" : "none";
    });

    // Lazily point the terminal iframe at its real src on first activation, so no
    // PTY is spawned until the Terminal tab is opened. (Class is screen-iframe —
    // it lives inside the titled screen frame.)
    var iframe = panels[tab].querySelector("iframe.screen-iframe");
    if (iframe && !iframe.getAttribute("src") && iframe.dataset.src) {
      iframe.setAttribute("src", iframe.dataset.src);
    }

    if (tab === "firewall" && !firewallStarted) {
      firewallStarted = true;
      startFirewallStream();
    }

    // startMitmFlows is defined in mitm.js (only loaded on the project page).
    if (tab === "mitm" && !mitmStarted && typeof startMitmFlows === "function") {
      mitmStarted = true;
      startMitmFlows(projectId);
    }

    // startConfig is defined in config.js; load the config panel on first open.
    if (tab === "config" && !configStarted && typeof startConfig === "function") {
      configStarted = true;
      startConfig(projectId);
    }
  }

  function startFirewallStream() {
    var logEl = document.getElementById("firewall-log");
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
    b.addEventListener("click", function () {
      activate(b.dataset.tab);
    });
  });

  // Activate the initially-selected tab on load so its iframe gets a src
  // immediately — otherwise the default (Terminal) tab renders blank/black until
  // the user clicks a tab to trigger the lazy src assignment in activate().
  var initial = document.querySelector(".tab-btn.active") || buttons[0];
  if (initial) activate(initial.dataset.tab);
})();
