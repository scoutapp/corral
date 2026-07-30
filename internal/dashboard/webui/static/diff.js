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
      '<div class="diff-side">' +
      '  <div class="diff-refs" id="diff-refs">' +
      '    <select id="diff-base" class="diff-ref" title="Base ref"></select>' +
      '    <span class="diff-refs-arrow">→</span>' +
      '    <select id="diff-target" class="diff-ref" title="Target ref"></select>' +
      '    <button id="diff-reset" type="button" title="Back to working-tree changes">✕</button>' +
      '  </div>' +
      '  <div class="diff-list" id="diff-list"><p class="muted">loading…</p></div>' +
      '</div>' +
      '<div class="diff-view"><pre id="diff-body" class="diff-body"><span class="muted">select a changed file</span></pre></div>';
    var listEl = document.getElementById("diff-list");
    var bodyEl = document.getElementById("diff-body");
    var baseSel = document.getElementById("diff-base");
    var targetSel = document.getElementById("diff-target");
    var resetBtn = document.getElementById("diff-reset");

    // Ref-diff state: when both base & target are set we diff base..target;
    // otherwise we show the working-tree changes (the original default).
    var base = "", target = "";
    function refsActive() { return base !== "" && target !== ""; }
    function refQuery() { return refsActive() ? "&base=" + encodeURIComponent(base) + "&target=" + encodeURIComponent(target) : ""; }

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
      fetch(api("/git/diff?path=" + encodeURIComponent(path) + refQuery()), { credentials: "same-origin" })
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

    // Load the changed-file list — working tree by default, or base..target when
    // both refs are chosen.
    function loadStatus() {
      listEl.innerHTML = '<p class="muted">loading…</p>';
      bodyEl.innerHTML = '<span class="muted">' + (refsActive() ? "select a changed file" : "select a changed file") + "</span>";
      resetBtn.style.display = refsActive() ? "" : "none";
      fetch(api("/git/status?_=1" + refQuery()), { credentials: "same-origin" })
        .then(function (r) { if (!r.ok) throw new Error("HTTP " + r.status); return r.json(); })
        .then(function (data) {
          if (!data.repo) { listEl.innerHTML = '<p class="muted">not a git repository</p>'; return; }
          var changes = data.changes || [];
          if (!changes.length) {
            listEl.innerHTML = '<p class="muted">' +
              (refsActive() ? "no differences between " + esc(base) + " and " + esc(target) : "working tree clean — no changes") +
              "</p>";
            bodyEl.innerHTML = '<span class="muted">no changed files</span>';
            return;
          }
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

    // Populate the base/target ref pickers. The first option is "working tree"
    // (empty) so you can always drop back to the default view.
    function opt(v, label) { return '<option value="' + esc(v) + '">' + esc(label || v) + "</option>"; }
    function loadRefs() {
      fetch(api("/git/refs"), { credentials: "same-origin" })
        .then(function (r) { if (!r.ok) throw new Error("HTTP " + r.status); return r.json(); })
        .then(function (data) {
          if (!data.repo) return;
          var refs = (data.branches || []).concat(data.tags || []);
          var head = '<option value="">— working tree —</option>';
          var options = head + refs.map(function (rf) { return opt(rf); }).join("");
          baseSel.innerHTML = options;
          targetSel.innerHTML = options;
        })
        .catch(function () { /* refs are optional; the default view still works */ });
    }

    function onRefChange() {
      base = baseSel.value;
      target = targetSel.value;
      // A single ref chosen implies the other side is HEAD/current — but we only
      // switch to ref mode once BOTH are set to avoid half-configured fetches.
      loadStatus();
    }
    baseSel.addEventListener("change", onRefChange);
    targetSel.addEventListener("change", onRefChange);
    resetBtn.addEventListener("click", function () {
      base = ""; target = "";
      baseSel.value = ""; targetSel.value = "";
      loadStatus();
    });

    loadRefs();
    loadStatus();
  }

  window.startDiff = startDiff;
})();
