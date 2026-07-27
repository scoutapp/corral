// Mitm tab: render mitmweb's /flows JSON (proxied server-side through
// /p/<id>/mitm/flows, see handleMitmFlows) as a native flow table, refreshed
// while the tab is visible. Replaces embedding mitmweb's SPA, which can't be
// reverse-proxied under a subpath.
//
// Exposed as a global (startMitmFlows) because dashboard.js triggers it on first
// activation of the Mitm tab, the same way it starts the firewall SSE stream.
function startMitmFlows(projectId) {
  var statusEl = document.getElementById("mitm-status");
  var flowsEl = document.getElementById("mitm-flows");
  if (!flowsEl) return;

  var POLL_MS = 2000;
  var lastRenderCount = -1;

  function fmtBytes(n) {
    if (n == null || n < 0) return "";
    if (n < 1024) return n + "b";
    if (n < 1024 * 1024) return (n / 1024).toFixed(1) + "kb";
    return (n / (1024 * 1024)).toFixed(1) + "mb";
  }

  // Request wall-clock duration in ms, from mitmweb's response timestamps.
  function fmtDuration(f) {
    var r = f.response;
    if (!r || !r.timestamp_end || !f.request || !f.request.timestamp_start) return "";
    var ms = Math.round((r.timestamp_end - f.request.timestamp_start) * 1000);
    return ms + "ms";
  }

  function statusClass(code) {
    if (!code) return "s-pending";
    if (code >= 500) return "s-5xx";
    if (code >= 400) return "s-4xx";
    if (code >= 300) return "s-3xx";
    return "s-2xx";
  }

  function render(flows) {
    // Newest first — mitmweb returns oldest-first.
    flows = flows.slice().reverse();

    if (flows.length === lastRenderCount) {
      // Cheap guard against re-painting an unchanged list every poll. Count is a
      // good-enough signal here since flows are append-only within a session.
      return;
    }
    lastRenderCount = flows.length;

    if (!flows.length) {
      flowsEl.innerHTML = '<p class="empty">No traffic captured yet.</p>';
      return;
    }

    var rows = flows.map(function (f) {
      var req = f.request || {};
      var resp = f.response || {};
      var host = req.pretty_host || req.host || "";
      var path = req.path || "";
      var status = resp.status_code || 0;
      var size = resp.contentLength != null ? resp.contentLength : req.contentLength;

      return (
        "<tr>" +
        '<td class="m-method">' + esc(req.method || "") + "</td>" +
        '<td class="m-host">' + esc(host) + "</td>" +
        '<td class="m-path" title="' + esc(path) + '">' + esc(path) + "</td>" +
        '<td class="m-status ' + statusClass(status) + '">' + (status || "…") + "</td>" +
        '<td class="m-size">' + esc(fmtBytes(size)) + "</td>" +
        '<td class="m-dur">' + esc(fmtDuration(f)) + "</td>" +
        "</tr>"
      );
    });

    flowsEl.innerHTML =
      '<table class="mitm-table"><thead><tr>' +
      "<th>Method</th><th>Host</th><th>Path</th><th>Status</th><th>Size</th><th>Time</th>" +
      "</tr></thead><tbody>" +
      rows.join("") +
      "</tbody></table>";
  }

  function esc(s) {
    return String(s)
      .replace(/&/g, "&amp;")
      .replace(/</g, "&lt;")
      .replace(/>/g, "&gt;")
      .replace(/"/g, "&quot;");
  }

  function poll() {
    fetch("/p/" + projectId + "/mitm/flows", { credentials: "same-origin" })
      .then(function (r) {
        if (!r.ok) throw new Error("HTTP " + r.status);
        return r.json();
      })
      .then(function (flows) {
        statusEl.textContent = flows.length + " flow" + (flows.length === 1 ? "" : "s");
        statusEl.className = "muted";
        render(flows);
      })
      .catch(function (err) {
        statusEl.textContent = "mitm unavailable: " + err.message;
        statusEl.className = "s-4xx";
      });
  }

  poll();
  // Poll only while the Mitm tab is the visible one — no point fetching flows for
  // a hidden panel. The panel's display style is toggled by dashboard.js.
  setInterval(function () {
    var panel = document.getElementById("tab-mitm");
    if (panel && panel.style.display !== "none") poll();
  }, POLL_MS);
}
