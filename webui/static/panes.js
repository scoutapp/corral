// Landing page: live "panes into work." Polls /status and renders one pane per
// project, sorted by urgency so the agent that needs YOU floats to the top:
// waiting-on-you first, then working, then idle. The grid re-sorts live as
// states change — the whole point is triage at a glance.
(function () {
  var panesEl = document.getElementById("panes");
  var summaryEl = document.getElementById("summary");
  var POLL_MS = 3000;

  // Urgency order for the sort: waiting-on-you is what demands attention.
  var RANK = { waiting: 0, working: 1, off: 2 };

  function esc(s) {
    return String(s == null ? "" : s).replace(/&/g, "&amp;").replace(/</g, "&lt;")
      .replace(/>/g, "&gt;").replace(/"/g, "&quot;");
  }

  function activityLabel(a) {
    if (a === "working") return "working";
    if (a === "waiting") return "waiting on you";
    return "idle";
  }

  function dot(up) { return '<i class="pdot ' + (up ? "on" : "off") + '"></i>'; }

  function pane(p) {
    var act = p.activity || "off";
    var peek = p.peek ? esc(p.peek) : (act === "off" ? "container not running" : "…");
    var rate = act === "working"
      ? '<span class="rate">' + p.anthropic_hits + " req/min · live</span>"
      : (act === "waiting" ? '<span class="rate quiet">idle at prompt</span>' : "");

    return '' +
      '<a class="pane pane-' + act + '" href="/p/' + esc(p.id) + '">' +
        '<div class="pane-top">' +
          '<span class="pane-name">' + esc(p.name) + "</span>" +
          '<span class="pane-state state-' + act + '">' +
            '<i class="beacon"></i>' + activityLabel(act) +
          "</span>" +
        "</div>" +
        '<div class="pane-peek"><span class="peek-caret">&gt;</span> ' + peek + "</div>" +
        '<div class="pane-foot">' +
          '<span class="svc">' + dot(p.container_up) + "box</span>" +
          '<span class="svc">' + dot(p.mitm_up) + "proxy</span>" +
          '<span class="svc">' + dot(p.tmux_up) + "session</span>" +
          rate +
        "</div>" +
      "</a>";
  }

  function summarize(projects) {
    var w = 0, q = 0, off = 0;
    projects.forEach(function (p) {
      if (p.activity === "working") w++;
      else if (p.activity === "waiting") q++;
      else off++;
    });
    var parts = [];
    if (q) parts.push(q + " waiting on you");
    if (w) parts.push(w + " working");
    if (off) parts.push(off + " idle");
    return projects.length + " project" + (projects.length === 1 ? "" : "s") +
      (parts.length ? " — " + parts.join(", ") : "");
  }

  function render(projects) {
    projects.sort(function (a, b) {
      var ra = RANK[a.activity] == null ? 3 : RANK[a.activity];
      var rb = RANK[b.activity] == null ? 3 : RANK[b.activity];
      if (ra !== rb) return ra - rb;
      return a.name.localeCompare(b.name);
    });

    if (!projects.length) return; // keep the server-rendered empty state

    panesEl.innerHTML = projects.map(pane).join("");
    summaryEl.textContent = summarize(projects);
    // Reflect the most urgent state on the footer for a peripheral cue.
    summaryEl.className = projects.some(function (p) { return p.activity === "waiting"; })
      ? "attention" : "muted";
  }

  function poll() {
    fetch("/status", { credentials: "same-origin" })
      .then(function (r) { if (!r.ok) throw new Error("HTTP " + r.status); return r.json(); })
      .then(function (data) { render(data.projects || []); })
      .catch(function (err) { summaryEl.textContent = "lost connection: " + err.message; summaryEl.className = "attention"; });
  }

  poll();
  setInterval(poll, POLL_MS);
})();
