// Cross-project toasts: when a project OTHER than the one you're viewing finishes
// (transitions working -> waiting-on-you), pop a dismissible toast linking to it.
// Polls the shared /status endpoint. Loaded on the project page (which otherwise
// has no notifications) and the landing page. The Ctrl-. hotkey clears all toasts.
(function () {
  var POLL_MS = 4000;

  // Which project are we currently viewing? Empty on the landing page.
  var currentId = (document.body && document.body.dataset.projectId) || "";

  // Toast container (bottom-right).
  var host = document.createElement("div");
  host.className = "toast-host";
  document.addEventListener("DOMContentLoaded", function () { document.body.appendChild(host); });
  if (document.body) document.body.appendChild(host);

  // Seed lastActivity on the first poll so we don't toast for state that was
  // already "waiting" when the page loaded.
  var lastActivity = null; // null until first poll completes
  var bootId = null;

  function clearAll() { host.innerHTML = ""; }
  document.addEventListener("sandclaude:clear-notifications", clearAll);

  function showToast(proj) {
    var t = document.createElement("a");
    t.className = "toast";
    t.href = "/p/" + proj.id + "/";
    t.innerHTML =
      '<span class="toast-dot"></span>' +
      '<span class="toast-body"><strong>' + escapeHtml(proj.name) + "</strong>" +
      '<span class="toast-sub">is waiting on you</span></span>' +
      '<button class="toast-x" title="Dismiss" aria-label="Dismiss">✕</button>';
    t.querySelector(".toast-x").addEventListener("click", function (e) {
      e.preventDefault(); e.stopPropagation(); t.remove();
    });
    host.appendChild(t);
    // Auto-dismiss after a while (but keep it long enough to notice/click).
    setTimeout(function () { if (t.isConnected) t.classList.add("toast-fade"); }, 12000);
    setTimeout(function () { if (t.isConnected) t.remove(); }, 13000);
  }

  function poll() {
    fetch("/status", { credentials: "same-origin" })
      .then(function (r) { return r.ok ? r.json() : null; })
      .then(function (data) {
        if (!data || !data.projects) return;
        // If the server rebooted, drop our edge memory (ids/state may reset).
        if (bootId !== null && data.boot_id !== bootId) lastActivity = null;
        bootId = data.boot_id;

        var seeding = lastActivity === null;
        if (seeding) lastActivity = {};
        data.projects.forEach(function (p) {
          var prev = lastActivity[p.id];
          var act = p.activity || "off";
          // The interesting edge: something finished and now wants you.
          if (!seeding && prev === "working" && act === "waiting" && p.id !== currentId) {
            showToast(p);
          }
          lastActivity[p.id] = act;
        });
      })
      .catch(function () { /* transient; try again next tick */ });
  }

  function escapeHtml(s) {
    return String(s == null ? "" : s).replace(/&/g, "&amp;").replace(/</g, "&lt;")
      .replace(/>/g, "&gt;").replace(/"/g, "&quot;");
  }

  poll();
  setInterval(poll, POLL_MS);
})();
