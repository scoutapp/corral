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
      '  <div class="diff-repo" id="diff-repo-row" style="display:none">' +
      '    <select id="diff-repo" class="diff-ref" title="Repository"></select>' +
      '  </div>' +
      '  <div class="diff-refs" id="diff-refs">' +
      '    <select id="diff-base" class="diff-ref" title="Base ref"></select>' +
      '    <span class="diff-refs-arrow">→</span>' +
      '    <select id="diff-target" class="diff-ref" title="Target ref"></select>' +
      '    <button id="diff-reset" type="button" title="Back to working-tree changes">✕</button>' +
      '  </div>' +
      '  <div class="diff-list" id="diff-list"><p class="muted">loading…</p></div>' +
      '</div>' +
      '<div class="diff-view" id="diff-view"><div id="diff-body" class="diff-body"><span class="muted">select a changed file</span></div></div>';
    var listEl = document.getElementById("diff-list");
    var bodyEl = document.getElementById("diff-body");
    var diffEditor = null; // live CodeMirror diff view (destroyed on each load)
    var repoRow = document.getElementById("diff-repo-row");
    var repoSel = document.getElementById("diff-repo");
    var baseSel = document.getElementById("diff-base");
    var targetSel = document.getElementById("diff-target");
    var resetBtn = document.getElementById("diff-reset");

    // repo = which git repo within the workspace to operate on ("" = root).
    // Refs: when both base & target are set we diff base..target; otherwise we
    // show the working-tree changes (the original default).
    var repo = "";
    var base = "", target = "";
    var refsAutoTried = false; // auto trunk..branch default is applied at most once
    function refsActive() { return base !== "" && target !== ""; }
    function refQuery() {
      var q = repo ? "&repo=" + encodeURIComponent(repo) : "";
      if (refsActive()) q += "&base=" + encodeURIComponent(base) + "&target=" + encodeURIComponent(target);
      return q;
    }

    function destroyDiffEditor() {
      if (diffEditor) { try { diffEditor.destroy(); } catch (e) {} diffEditor = null; }
    }
    function diffMessage(html) {
      destroyDiffEditor();
      bodyEl.innerHTML = html;
    }

    // Render a syntax-highlighted diff of one file using the bundled CodeMirror
    // diff view (unifiedMergeView): the code is language-colored and changed
    // lines/words carry inline add/remove decorations. Falls back to a plain
    // colorized unified diff if the editor bundle isn't available.
    function loadDiff(path) {
      diffMessage('<span class="muted">loading diff…</span>');
      fetch(api("/git/file?path=" + encodeURIComponent(path) + refQuery()), { credentials: "same-origin" })
        .then(function (r) { if (!r.ok) throw new Error("HTTP " + r.status); return r.json(); })
        .then(function (data) {
          if ((data.original || "") === (data.modified || "")) {
            diffMessage('<span class="muted">no differences in this file</span>');
            return;
          }
          if (!window.SandclaudeEditor || !window.SandclaudeEditor.createDiff) {
            return loadDiffFallback(path); // no bundle — plain text diff
          }
          destroyDiffEditor();
          bodyEl.innerHTML = "";
          diffEditor = window.SandclaudeEditor.createDiff({
            parent: bodyEl,
            original: data.original || "",
            modified: data.modified || "",
            filename: data.filename || path,
          });
        })
        .catch(function (err) { diffMessage('<span class="attention">diff error: ' + esc(err.message) + "</span>"); });
    }

    // Plain colorized unified-diff fallback (used only if the CM bundle is
    // missing). Colorizes a unified diff into per-line spans.
    function loadDiffFallback(path) {
      fetch(api("/git/diff?path=" + encodeURIComponent(path) + refQuery()), { credentials: "same-origin" })
        .then(function (r) { if (!r.ok) throw new Error("HTTP " + r.status); return r.text(); })
        .then(function (text) {
          if (!text.trim()) { bodyEl.innerHTML = '<span class="muted">no differences</span>'; return; }
          bodyEl.innerHTML = '<pre class="diff-pre">' + text.split("\n").map(function (line) {
            var cls = "";
            if (line[0] === "+" && line.slice(0, 3) !== "+++") cls = "d-add";
            else if (line[0] === "-" && line.slice(0, 3) !== "---") cls = "d-del";
            else if (line[0] === "@") cls = "d-hunk";
            else if (line.slice(0, 4) === "diff" || line.slice(0, 3) === "+++" || line.slice(0, 3) === "---") cls = "d-meta";
            return '<span class="' + cls + '">' + esc(line) + "</span>";
          }).join("\n") + "</pre>";
        })
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
      // Scope to the selected repo — without ?repo=, a non-repo workspace root
      // returns repo:false and the ref dropdowns stay empty ("can't select
      // anything"). refQuery() already carries repo (and base/target, harmless
      // here since /git/refs ignores them).
      var q = repo ? "?repo=" + encodeURIComponent(repo) : "";
      fetch(api("/git/refs" + q), { credentials: "same-origin" })
        .then(function (r) { if (!r.ok) throw new Error("HTTP " + r.status); return r.json(); })
        .then(function (data) {
          if (!data.repo) {
            baseSel.innerHTML = '<option value="">— no git —</option>';
            targetSel.innerHTML = baseSel.innerHTML;
            return;
          }
          var branches = data.branches || [];
          var refs = branches.concat(data.tags || []);
          var head = '<option value="">— working tree —</option>';
          var options = head + refs.map(function (rf) { return opt(rf); }).join("");
          baseSel.innerHTML = options;
          targetSel.innerHTML = options;

          // Default to trunk..<current-branch> when the checked-out branch is a
          // feature/issue branch (not the trunk itself) and a trunk exists. This
          // makes issue-spawned projects open the Diff tab already showing the
          // branch's work vs. the base, instead of working-tree-vs-HEAD. Only when
          // the user hasn't already picked refs.
          if (!refsAutoTried && !refsActive() && branches.length > 0) {
            refsAutoTried = true; // only after a real repo with branches responds
            var cur = data.current || "";
            var trunk = ["main", "master", "trunk"].filter(function (t) {
              return branches.indexOf(t) >= 0;
            })[0];
            if (cur && trunk && cur !== trunk) {
              base = trunk; target = cur;
              baseSel.value = trunk; targetSel.value = cur;
              loadStatus();
            }
          }
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

    // A workspace can hold multiple repos (submodules, or sibling project dirs).
    // Show a repo picker when there's more than one option; switching repos
    // resets the ref selection and reloads that repo's refs + changes.
    function loadRepos() {
      fetch(api("/git/repos"), { credentials: "same-origin" })
        .then(function (r) { if (!r.ok) throw new Error("HTTP " + r.status); return r.json(); })
        .then(function (data) {
          var repos = data.repos || [];
          // Only surface the picker when there's a real choice to make.
          if (repos.length <= 1) {
            repoRow.style.display = "none";
            // A single repo that ISN'T the workspace root (e.g. a spawned/cloned
            // project has its repo in a SUBDIR) — point at it, else diff/status
            // run against the non-git root and show nothing.
            if (repos.length === 1 && repos[0].path && !data.rootIsRepo && repos[0].path !== repo) {
              repo = repos[0].path;
              loadRefs(); loadStatus();
            }
            return;
          }
          repoRow.style.display = "";
          repoSel.innerHTML = repos.map(function (rp) { return opt(rp.path, rp.name); }).join("");
          // Default to the root repo if present, else the first detected repo.
          // If that isn't the root (""), the initial refs/status ran against the
          // wrong repo, so reload them for the chosen one.
          var def = data.rootIsRepo ? "" : repos[0].path;
          repoSel.value = def;
          if (def !== repo) { repo = def; loadRefs(); loadStatus(); }
        })
        .catch(function () { /* single-repo / non-repo: default view still works */ });
    }
    repoSel.addEventListener("change", function () {
      repo = repoSel.value;
      base = ""; target = "";           // refs are per-repo — reset the range
      loadRefs();
      loadStatus();
    });

    loadRepos();
    loadRefs();
    loadStatus();
  }

  window.startDiff = startDiff;
})();
