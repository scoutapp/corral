// Mitm tab: render mitmweb's /flows JSON (proxied server-side through
// /p/<id>/mitm/flows, see handleMitmFlows) as a native, filterable flow table
// with click-to-expand payload inspection. Replaces embedding mitmweb's SPA,
// which can't be reverse-proxied under a subpath.
//
// Exposed as a global (startMitmFlows) because dashboard.js triggers it on first
// activation of the Mitm tab, the same way it starts the firewall SSE stream.
function startMitmFlows(projectId) {
  var statusEl = document.getElementById("mitm-status");
  var flowsEl = document.getElementById("mitm-flows");
  var filterEl = document.getElementById("mitm-filter");
  if (!flowsEl) return;

  var POLL_MS = 2000;
  var flows = []; // newest-first, as rendered
  var expanded = {}; // flowID -> true, so expansion survives re-render on poll
  var bodyCache = {}; // "id:side" -> loaded body text, to avoid refetching
  var lastCount = -1; // last rendered flow count, to skip redundant poll repaints

  function esc(s) {
    return String(s)
      .replace(/&/g, "&amp;").replace(/</g, "&lt;")
      .replace(/>/g, "&gt;").replace(/"/g, "&quot;");
  }

  function fmtBytes(n) {
    if (n == null || n < 0) return "";
    if (n < 1024) return n + "b";
    if (n < 1024 * 1024) return (n / 1024).toFixed(1) + "kb";
    return (n / (1024 * 1024)).toFixed(1) + "mb";
  }

  function fmtDuration(f) {
    var r = f.response;
    if (!r || !r.timestamp_end || !f.request || !f.request.timestamp_start) return "";
    return Math.round((r.timestamp_end - f.request.timestamp_start) * 1000) + "ms";
  }

  // Wall-clock time a flow started, as local HH:MM:SS. mitmweb reports
  // timestamp_created in epoch seconds (float); the request start is used when
  // present since it's the moment the request actually went out.
  function fmtWhen(f) {
    var t = (f.request && f.request.timestamp_start) || f.timestamp_created;
    if (!t) return "";
    var d = new Date(t * 1000);
    var p = function (n) { return (n < 10 ? "0" : "") + n; };
    return p(d.getHours()) + ":" + p(d.getMinutes()) + ":" + p(d.getSeconds());
  }

  function statusClass(code) {
    if (!code) return "s-pending";
    if (code >= 500) return "s-5xx";
    if (code >= 400) return "s-4xx";
    if (code >= 300) return "s-3xx";
    return "s-2xx";
  }

  // A flow matches the filter if any of method/host/path/status contains the
  // (case-insensitive) query. Space-separated terms are ANDed.
  function matches(f, terms) {
    if (!terms.length) return true;
    var req = f.request || {}, resp = f.response || {};
    var hay = [
      req.method, req.pretty_host || req.host, req.path,
      resp.status_code,
    ].join(" ").toLowerCase();
    return terms.every(function (t) { return hay.indexOf(t) !== -1; });
  }

  function headerTable(headers) {
    if (!headers || !headers.length) return '<div class="mitm-empty">(none)</div>';
    return '<table class="mitm-headers">' + headers.map(function (h) {
      return "<tr><td>" + esc(h[0]) + "</td><td>" + esc(h[1]) + "</td></tr>";
    }).join("") + "</table>";
  }

  // Pretty-print JSON bodies; leave other text as-is. Called on already-fetched
  // body text.
  function prettyBody(text, contentType) {
    if (text === "") return '<div class="mitm-empty">(empty)</div>';
    var ct = (contentType || "").toLowerCase();
    if (ct.indexOf("json") !== -1) {
      try { text = JSON.stringify(JSON.parse(text), null, 2); } catch (e) { /* leave raw */ }
    }
    return '<pre class="mitm-body">' + esc(text) + "</pre>";
  }

  function contentTypeOf(msg) {
    var hs = (msg && msg.headers) || [];
    for (var i = 0; i < hs.length; i++) {
      if (hs[i][0].toLowerCase() === "content-type") return hs[i][1];
    }
    return "";
  }

  // Fetch and render a body into its placeholder, once. Large/binary bodies are
  // still fetched — mitmweb returns them as text; we cap what we render to avoid
  // freezing the tab on a multi-MB payload.
  var BODY_RENDER_CAP = 512 * 1024;
  function loadBody(flowID, side, msg, el) {
    var key = flowID + ":" + side;
    if (bodyCache[key] != null) {
      el.innerHTML = prettyBody(bodyCache[key], contentTypeOf(msg));
      return;
    }
    var len = msg && msg.contentLength;
    if (len === 0) { el.innerHTML = prettyBody("", contentTypeOf(msg)); return; }
    el.innerHTML = '<div class="mitm-empty">loading…</div>';
    fetch("/p/" + projectId + "/mitm/flows/" + flowID + "/" + side + "/content",
          { credentials: "same-origin" })
      .then(function (r) { return r.text(); })
      .then(function (text) {
        if (text.length > BODY_RENDER_CAP) {
          el.innerHTML = '<div class="mitm-empty">body too large to display (' +
            fmtBytes(text.length) + ") — inspect via mitmweb directly</div>";
          return;
        }
        bodyCache[key] = text;
        el.innerHTML = prettyBody(text, contentTypeOf(msg));
      })
      .catch(function (err) {
        el.innerHTML = '<div class="mitm-empty">failed to load body: ' + esc(err.message) + "</div>";
      });
  }

  function detailHTML(f) {
    var req = f.request || {}, resp = f.response || {};
    return (
      '<div class="mitm-detail">' +
        '<div class="mitm-sec">Request headers</div>' + headerTable(req.headers) +
        '<div class="mitm-sec">Request body</div>' +
          '<div class="mitm-bodyslot" data-side="request"></div>' +
        '<div class="mitm-sec">Response headers</div>' + headerTable(resp.headers) +
        '<div class="mitm-sec">Response body</div>' +
          '<div class="mitm-bodyslot" data-side="response"></div>' +
      "</div>"
    );
  }

  function render() {
    var terms = filterEl.value.trim().toLowerCase().split(/\s+/).filter(Boolean);
    var shown = flows.filter(function (f) { return matches(f, terms); });

    if (!flows.length) {
      flowsEl.innerHTML = '<p class="empty">No traffic captured yet.</p>';
      return;
    }
    if (!shown.length) {
      flowsEl.innerHTML = '<p class="empty">No flows match “' + esc(filterEl.value) + '”.</p>';
      return;
    }

    var rowsHTML = shown.map(function (f) {
      var req = f.request || {}, resp = f.response || {};
      var status = resp.status_code || 0;
      var size = resp.contentLength != null ? resp.contentLength : req.contentLength;
      var open = !!expanded[f.id];
      var main =
        '<tr class="m-row" data-id="' + esc(f.id) + '">' +
          '<td class="m-when">' + esc(fmtWhen(f)) + "</td>" +
          '<td class="m-caret">' + (open ? "▾" : "▸") + "</td>" +
          '<td class="m-method">' + esc(req.method || "") + "</td>" +
          '<td class="m-host">' + esc(req.pretty_host || req.host || "") + "</td>" +
          '<td class="m-path" title="' + esc(req.path || "") + '">' + esc(req.path || "") + "</td>" +
          '<td class="m-status ' + statusClass(status) + '">' + (status || "…") + "</td>" +
          '<td class="m-size">' + esc(fmtBytes(size)) + "</td>" +
          '<td class="m-dur">' + esc(fmtDuration(f)) + "</td>" +
        "</tr>";
      var detail = open
        ? '<tr class="m-detailrow" data-id="' + esc(f.id) + '"><td colspan="8">' + detailHTML(f) + "</td></tr>"
        : "";
      return main + detail;
    }).join("");

    flowsEl.innerHTML =
      '<table class="mitm-table"><thead><tr>' +
      "<th>When</th><th></th><th>Method</th><th>Host</th><th>Path</th><th>Status</th><th>Size</th><th>Dur</th>" +
      "</tr></thead><tbody>" + rowsHTML + "</tbody></table>";

    // Populate bodies for any currently-expanded rows.
    shown.forEach(function (f) {
      if (!expanded[f.id]) return;
      var detailRow = flowsEl.querySelector('.m-detailrow[data-id="' + cssEscape(f.id) + '"]');
      if (!detailRow) return;
      detailRow.querySelectorAll(".mitm-bodyslot").forEach(function (slot) {
        var side = slot.dataset.side;
        loadBody(f.id, side, side === "request" ? f.request : f.response, slot);
      });
    });
  }

  // Minimal attribute-selector escaping for UUID-ish ids (no exotic chars here,
  // but be safe if mitmweb ever changes id format).
  function cssEscape(s) { return s.replace(/["\\]/g, "\\$&"); }

  flowsEl.addEventListener("click", function (e) {
    var row = e.target.closest(".m-row");
    if (!row) return;
    var fid = row.dataset.id;
    expanded[fid] = !expanded[fid];
    render();
  });

  var filterTimer;
  filterEl.addEventListener("input", function () {
    clearTimeout(filterTimer);
    filterTimer = setTimeout(render, 120);
  });

  function poll() {
    fetch("/p/" + projectId + "/mitm/flows", { credentials: "same-origin" })
      .then(function (r) {
        if (!r.ok) throw new Error("HTTP " + r.status);
        return r.json();
      })
      .then(function (data) {
        flows = data.slice().reverse(); // newest first
        statusEl.textContent = flows.length + " flow" + (flows.length === 1 ? "" : "s");
        statusEl.className = "muted";
        // Only repaint when the flow list actually grew — avoids clobbering a
        // user's text selection / scroll in an expanded body every 2s. Filter
        // input and row clicks call render() directly and always repaint.
        if (flows.length !== lastCount) {
          lastCount = flows.length;
          render();
        }
      })
      .catch(function (err) {
        statusEl.textContent = "mitm unavailable: " + err.message;
        statusEl.className = "s-4xx";
      });
  }

  poll();
  setInterval(function () {
    var panel = document.getElementById("tab-mitm");
    if (panel && panel.style.display !== "none") poll();
  }, POLL_MS);
}
