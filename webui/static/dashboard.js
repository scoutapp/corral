(function () {
  var projectId = document.body.dataset.projectId;
  if (!projectId) return; // landing page, nothing to wire up

  var buttons = document.querySelectorAll(".tab-btn");
  var panels = {
    terminal: document.getElementById("tab-terminal"),
    mitm: document.getElementById("tab-mitm"),
    firewall: document.getElementById("tab-firewall"),
  };
  var firewallStarted = false;

  function activate(tab) {
    buttons.forEach(function (b) {
      b.classList.toggle("active", b.dataset.tab === tab);
    });
    Object.keys(panels).forEach(function (key) {
      panels[key].style.display = key === tab ? "block" : "none";
    });

    // Lazily point iframes at their real src on first activation, so ttyd/mitmweb
    // aren't touched (and no ttyd process is spawned) until the tab is opened.
    var iframe = panels[tab].querySelector("iframe.tab-iframe");
    if (iframe && !iframe.getAttribute("src") && iframe.dataset.src) {
      iframe.setAttribute("src", iframe.dataset.src);
    }

    if (tab === "firewall" && !firewallStarted) {
      firewallStarted = true;
      startFirewallStream();
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
})();
