// Diff tab: what has Claude changed? Lists the workspace's git working-tree
// changes and shows the unified diff for a selected file, colorized. Pairs with
// handleGitStatus / handleGitDiff in dashboard_files.go.
(function () {
  function startDiff(projectId) {
    var root = document.getElementById("diff-root");
    if (!root) return;

    function api(p) { return "/p/" + projectId + p; }
    function esc(s) {
      return String(s == null ? "" : s).replace(/&/g, "&amp;").replace(/</g, "&lt;")
        .replace(/>/g, "&gt;").replace(/"/g, "&quot;");
    }

    root.innerHTML =
      '<div class="diff-list" id="diff-list"><p class="muted">loading…</p></div>' +
      '<div class="diff-view"><pre id="diff-body" class="diff-body"><span class="muted">select a changed file</span></pre></div>';
    var listEl = document.getElementById("diff-list");
    var bodyEl = document.getElementById("diff-body");

    // Colorize a unified diff into per-line spans.
    function renderDiff(text) {
      if (!text.trim()) { bodyEl.innerHTML = '<span class="muted">no differences</span>'; return; }
      var html = text.split("\n").map(function (line) {
        var cls = "";
        if (line[0] === "+" && line.slice(0, 3) !== "+++") cls = "d-add";
        else if (line[0] === "-" && line.slice(0, 3) !== "---") cls = "d-del";
        else if (line[0] === "@") cls = "d-hunk";
        else if (line.slice(0, 4) === "diff" || line.slice(0, 3) === "+++" || line.slice(0, 3) === "---") cls = "d-meta";
        return '<span class="' + cls + '">' + esc(line) + "</span>";
      }).join("\n");
      bodyEl.innerHTML = html;
    }

    function loadDiff(path) {
      bodyEl.innerHTML = '<span class="muted">loading diff…</span>';
      fetch(api("/git/diff?path=" + encodeURIComponent(path)), { credentials: "same-origin" })
        .then(function (r) { if (!r.ok) throw new Error("HTTP " + r.status); return r.text(); })
        .then(renderDiff)
        .catch(function (err) { bodyEl.innerHTML = '<span class="attention">diff error: ' + esc(err.message) + "</span>"; });
    }

    function statusLabel(xy) {
      var t = xy.trim();
      if (t === "??") return "new";
      if (t.indexOf("M") >= 0) return "modified";
      if (t.indexOf("A") >= 0) return "added";
      if (t.indexOf("D") >= 0) return "deleted";
      if (t.indexOf("R") >= 0) return "renamed";
      return t || "changed";
    }

    fetch(api("/git/status"), { credentials: "same-origin" })
      .then(function (r) { if (!r.ok) throw new Error("HTTP " + r.status); return r.json(); })
      .then(function (data) {
        if (!data.repo) { listEl.innerHTML = '<p class="muted">not a git repository</p>'; return; }
        var changes = data.changes || [];
        if (!changes.length) { listEl.innerHTML = '<p class="muted">working tree clean — no changes</p>'; return; }
        listEl.innerHTML = "";
        changes.forEach(function (c, i) {
          var row = document.createElement("div");
          row.className = "diff-file";
          var stat = (c.added || c.removed)
            ? '<span class="d-stat"><span class="d-add">+' + c.added + "</span> <span class=\"d-del\">-" + c.removed + "</span></span>"
            : "";
          row.innerHTML =
            '<span class="d-status">' + esc(statusLabel(c.status)) + "</span>" +
            '<span class="d-path">' + esc(c.path) + "</span>" + stat;
          row.addEventListener("click", function () {
            listEl.querySelectorAll(".diff-file").forEach(function (el) { el.classList.remove("active"); });
            row.classList.add("active");
            loadDiff(c.path);
          });
          listEl.appendChild(row);
          if (i === 0) { row.classList.add("active"); loadDiff(c.path); }
        });
      })
      .catch(function (err) { listEl.innerHTML = '<p class="attention">git error: ' + esc(err.message) + "</p>"; });
  }

  window.startDiff = startDiff;
})();
