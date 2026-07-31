// Files tab: a lazy directory tree on the left, a CodeMirror editor on the right.
// Reads/writes go straight to the host workspace (which is bind-mounted into the
// container), so edits here are immediately visible to Claude. Pairs with the
// handleFilesTree/Read/Write handlers in dashboard_files.go.
(function () {
  function startFiles(projectId) {
    var root = document.getElementById("files-root");
    if (!root) return;

    root.innerHTML =
      '<div class="files-side">' +
      '  <div class="files-search">' +
      '    <input id="files-q" type="text" placeholder="search files…" autocomplete="off" spellcheck="false">' +
      '    <div class="files-search-mode">' +
      '      <button id="mode-name" class="active" type="button" title="Find files by name">name</button>' +
      '      <button id="mode-grep" type="button" title="Search file contents (grep)">grep</button>' +
      '      <button id="files-refresh" type="button" title="Refresh the file tree">⟳</button>' +
      '    </div>' +
      '  </div>' +
      '  <div class="files-tree" id="files-tree"></div>' +
      '  <div class="files-results" id="files-results" style="display:none"></div>' +
      '</div>' +
      '<div class="files-editor">' +
      '  <div class="files-editor-bar">' +
      '    <span id="files-current" class="muted">select a file</span>' +
      '    <button id="files-save" type="button" disabled>Save</button>' +
      '  </div>' +
      '  <div id="files-cm" class="files-cm"></div>' +
      "</div>";

    var qEl = document.getElementById("files-q");
    var modeNameBtn = document.getElementById("mode-name");
    var modeGrepBtn = document.getElementById("mode-grep");
    var resultsEl = document.getElementById("files-results");
    var treeEl = document.getElementById("files-tree");
    var currentEl = document.getElementById("files-current");
    var saveBtn = document.getElementById("files-save");
    var cmHost = document.getElementById("files-cm");

    var editor = null;      // SandclaudeEditor handle
    var openPath = null;    // workspace-relative path of the open file
    var dirty = false;

    function api(path) { return "/p/" + projectId + path; }
    function esc(s) {
      return String(s == null ? "" : s).replace(/&/g, "&amp;").replace(/</g, "&lt;")
        .replace(/>/g, "&gt;").replace(/"/g, "&quot;");
    }

    // File-type icon: a small colored monogram keyed by extension. Inline (no
    // external icon font — the dashboard's CSP blocks CDNs). The glyph is a short
    // label; color follows the language's conventional accent.
    var ICONS = {
      js: ["JS", "#f0db4f"], jsx: ["JS", "#f0db4f"], mjs: ["JS", "#f0db4f"], cjs: ["JS", "#f0db4f"],
      ts: ["TS", "#3178c6"], tsx: ["TS", "#3178c6"],
      go: ["GO", "#00add8"], py: ["PY", "#3572a5"], rb: ["RB", "#cc342d"], rs: ["RS", "#dea584"],
      java: ["JV", "#b07219"], c: ["C", "#555555"], h: ["H", "#555555"], cpp: ["C+", "#f34b7d"],
      json: ["{}", "#cbcb41"], yml: ["YM", "#cb171e"], yaml: ["YM", "#cb171e"], toml: ["TO", "#9c4221"],
      md: ["MD", "#519aba"], markdown: ["MD", "#519aba"], txt: ["TX", "#9aa4b0"],
      html: ["<>", "#e34c26"], htm: ["<>", "#e34c26"], css: ["#", "#563d7c"], scss: ["#", "#c6538c"],
      sh: ["SH", "#89e051"], bash: ["SH", "#89e051"], zsh: ["SH", "#89e051"],
      dockerfile: ["DK", "#384d54"], sql: ["SQ", "#e38c00"], xml: ["</", "#e34c26"],
      png: ["IM", "#a074c4"], jpg: ["IM", "#a074c4"], jpeg: ["IM", "#a074c4"], gif: ["IM", "#a074c4"], svg: ["IM", "#ffb13b"],
      lock: ["LK", "#9aa4b0"], env: ["EV", "#e2c08d"], gitignore: ["GI", "#f14e32"],
    };
    function extOf(name) {
      var n = name.toLowerCase();
      if (n === "dockerfile") return "dockerfile";
      if (n === ".gitignore") return "gitignore";
      if (n === ".env" || n.indexOf(".env.") === 0) return "env";
      var i = n.lastIndexOf(".");
      return i > 0 ? n.slice(i + 1) : "";
    }
    function fileIcon(name) {
      var def = ICONS[extOf(name)] || ["·", "#8a94a6"];
      return '<span class="ficon" style="color:' + def[1] + '">' + def[0] + "</span>";
    }
    var folderIcon = '<span class="ficon ficon-dir">▸</span>';

    function setDirty(d) {
      dirty = d;
      saveBtn.disabled = !d || openPath == null;
      currentEl.textContent = (openPath || "select a file") + (d ? " •" : "");
    }

    // expandedDirs maps a directory's relPath -> the container element holding its
    // rendered <ul>. Used both to remember what's open (for collapse) and to let
    // the auto-refresh poll re-read exactly the currently-open directories.
    var expandedDirs = {};

    // Build one directory-entry <li>. Directories expand/collapse lazily; the
    // expand handler records the open dir in expandedDirs so the poller can
    // refresh it and so state survives a reconcile.
    function makeEntryLi(e, relPath) {
      var li = document.createElement("li");
      var childRel = relPath ? relPath + "/" + e.name : e.name;
      li.dataset.name = e.name;
      if (e.dir) {
        li.className = "ftree-dir collapsed";
        li.dataset.dir = "1";
        li.innerHTML =
          '<span class="ftree-label">' + folderIcon +
          '<span class="ftree-name">' + esc(e.name) + "</span></span>";
        li.querySelector(".ftree-label").addEventListener("click", function (ev) {
          ev.stopPropagation();
          var sub = li._sub;
          if (sub) { sub.remove(); li._sub = null; delete expandedDirs[childRel]; li.classList.add("collapsed"); return; }
          li.classList.remove("collapsed");
          sub = document.createElement("div");
          li._sub = sub;
          li.appendChild(sub);
          expandedDirs[childRel] = sub;
          renderDir(sub, childRel);
        });
      } else {
        li.className = "ftree-file";
        li.dataset.path = childRel;
        li.innerHTML =
          '<span class="ftree-label">' + fileIcon(e.name) +
          '<span class="ftree-name">' + esc(e.name) + "</span></span>";
        li.querySelector(".ftree-label").addEventListener("click", function (ev) {
          ev.stopPropagation();
          openFile(childRel);
        });
      }
      li.querySelector(".ftree-label").addEventListener("contextmenu", function (ev) {
        ev.preventDefault(); ev.stopPropagation();
        openContextMenu(ev.clientX, ev.clientY, childRel, !!e.dir);
      });
      return li;
    }

    // Render a directory's entries into parentEl. Reconciles against any existing
    // <ul> so a refresh adds new entries and drops deleted ones WITHOUT collapsing
    // open subfolders, losing the active-file highlight, or resetting scroll.
    function renderDir(parentEl, relPath) {
      fetch(api("/files/tree?path=" + encodeURIComponent(relPath)), { credentials: "same-origin" })
        .then(function (r) { if (!r.ok) throw new Error("HTTP " + r.status); return r.json(); })
        .then(function (data) {
          var entries = data.entries || [];
          var ul = parentEl.querySelector(":scope > ul");
          if (!ul) { ul = document.createElement("ul"); parentEl.appendChild(ul); }

          // Index existing <li> by name so we can keep the ones that still exist.
          var existing = {};
          Array.prototype.forEach.call(ul.children, function (li) { existing[li.dataset.name] = li; });

          var wanted = {};
          entries.forEach(function (e, i) {
            wanted[e.name] = true;
            var li = existing[e.name];
            if (!li) {
              // New entry — insert at its sorted position (entries are pre-sorted).
              li = makeEntryLi(e, relPath);
              var ref = ul.children[i] || null;
              ul.insertBefore(li, ref);
            }
          });
          // Remove entries that disappeared on disk (and forget any expansions).
          Array.prototype.slice.call(ul.children).forEach(function (li) {
            if (!wanted[li.dataset.name]) {
              if (li.dataset.path) { /* file */ }
              ul.removeChild(li);
            }
          });
        })
        .catch(function (err) {
          if (!parentEl.querySelector(":scope > ul")) {
            parentEl.innerHTML = '<p class="attention">tree error: ' + esc(err.message) + "</p>";
          }
        });
    }

    // ---- Filesystem ops (create / mkdir / rename / delete) ------------------
    function fpost(path) { return fetch(api(path), { method: "POST", credentials: "same-origin" }); }
    function opThen(promise, okMsg) {
      return promise.then(function (r) {
        if (!r.ok) return r.text().then(function (t) { throw new Error(t || ("HTTP " + r.status)); });
        refreshTree();
      }).catch(function (e) { alert((okMsg || "operation") + " failed: " + e.message); });
    }
    // parentRel of "a/b/c" -> "a/b"; "" for a top-level name.
    function parentOf(rel) { var i = rel.lastIndexOf("/"); return i < 0 ? "" : rel.slice(0, i); }
    function join(dir, name) { return dir ? dir + "/" + name : name; }

    function doNewFile(dirRel) {
      var name = window.prompt("New file name:");
      if (!name) return;
      opThen(fpost("/files/new?path=" + encodeURIComponent(join(dirRel, name))), "create file")
        .then(function () { openFile(join(dirRel, name)); });
    }
    function doNewFolder(dirRel) {
      var name = window.prompt("New folder name:");
      if (!name) return;
      opThen(fpost("/files/mkdir?path=" + encodeURIComponent(join(dirRel, name))), "create folder");
    }
    function doRename(rel) {
      var base = rel.split("/").pop();
      var name = window.prompt("Rename to:", base);
      if (!name || name === base) return;
      var to = join(parentOf(rel), name);
      opThen(fpost("/files/rename?from=" + encodeURIComponent(rel) + "&to=" + encodeURIComponent(to)), "rename");
    }
    function doDelete(rel, isDir) {
      if (!window.confirm("Delete " + (isDir ? "folder" : "file") + " '" + rel + "'?" + (isDir ? " (recursive)" : ""))) return;
      opThen(fetch(api("/files?path=" + encodeURIComponent(rel)), { method: "DELETE", credentials: "same-origin" }), "delete");
    }

    // ---- Right-click context menu -------------------------------------------
    var ctxMenu = null;
    function closeContextMenu() { if (ctxMenu) { ctxMenu.remove(); ctxMenu = null; } }
    document.addEventListener("click", closeContextMenu);
    document.addEventListener("keydown", function (e) { if (e.key === "Escape") closeContextMenu(); });

    // rel="" + isDir=true means the tree root (create at workspace top level).
    function openContextMenu(x, y, rel, isDir) {
      closeContextMenu();
      var items = [];
      if (isDir) {
        items.push(["New file", function () { doNewFile(rel); }]);
        items.push(["New folder", function () { doNewFolder(rel); }]);
      }
      if (rel !== "") { // can't rename/delete the workspace root
        items.push(["Rename", function () { doRename(rel); }]);
        items.push(["Delete", function () { doDelete(rel, isDir); }]);
      }
      if (!items.length) return;
      var menu = document.createElement("div");
      menu.className = "ctx-menu";
      items.forEach(function (it) {
        var el = document.createElement("div");
        el.className = "ctx-item";
        el.textContent = it[0];
        el.addEventListener("click", function (ev) { ev.stopPropagation(); closeContextMenu(); it[1](); });
        menu.appendChild(el);
      });
      menu.style.left = x + "px"; menu.style.top = y + "px";
      document.body.appendChild(menu);
      ctxMenu = menu;
    }
    // Right-clicking the empty tree area = operate on the workspace root.
    treeEl.addEventListener("contextmenu", function (ev) {
      if (ev.target.closest(".ftree-label")) return; // handled per-row
      ev.preventDefault();
      openContextMenu(ev.clientX, ev.clientY, "", true);
    });

    // Re-read every currently-expanded directory (plus the root) so the tree
    // tracks Claude's filesystem changes without a manual collapse/expand.
    function refreshTree() {
      renderDir(treeEl, "");
      Object.keys(expandedDirs).forEach(function (rel) {
        var container = expandedDirs[rel];
        if (container && container.isConnected) renderDir(container, rel);
        else delete expandedDirs[rel];
      });
      if (openPath) markActive(openPath);
    }

    function markActive(rel) {
      treeEl.querySelectorAll(".ftree-file.active").forEach(function (el) { el.classList.remove("active"); });
      var node = treeEl.querySelector('.ftree-file[data-path="' + (window.CSS && CSS.escape ? CSS.escape(rel) : rel) + '"]');
      if (node) node.classList.add("active");
    }

    // openFile(rel[, line]) — open a file; if line (1-based) is given, scroll to
    // and select that line (used by grep results to jump to the match).
    function openFile(rel, line) {
      if (dirty && !window.confirm("Discard unsaved changes to " + openPath + "?")) return;
      markActive(rel);
      fetch(api("/files/read?path=" + encodeURIComponent(rel)), { credentials: "same-origin" })
        .then(function (r) { if (!r.ok) throw new Error("HTTP " + r.status); return r.json(); })
        .then(function (data) {
          if (data.too_large) {
            cmHost.innerHTML = '<p class="muted" style="padding:1rem">file is too large to edit here (' + data.size + " bytes)</p>";
            if (editor) { editor.destroy(); editor = null; }
            openPath = rel; setDirty(false); saveBtn.disabled = true;
            return;
          }
          if (editor) editor.destroy();
          cmHost.innerHTML = "";
          openPath = rel;
          editor = window.SandclaudeEditor.createEditor({
            parent: cmHost,
            doc: data.content || "",
            filename: data.filename || rel,
            onChange: function () { if (!dirty) setDirty(true); },
          });
          setDirty(false);
          if (line && editor.view) revealLine(editor.view, line);
        })
        .catch(function (err) { cmHost.innerHTML = '<p class="attention" style="padding:1rem">read error: ' + esc(err.message) + "</p>"; });
    }

    // Scroll a CodeMirror view to a 1-based line and put the cursor there.
    function revealLine(view, line) {
      try {
        var info = view.state.doc.line(Math.max(1, Math.min(line, view.state.doc.lines)));
        view.dispatch({
          selection: { anchor: info.from },
          effects: window.SandclaudeEditor.scrollToLineEffect
            ? window.SandclaudeEditor.scrollToLineEffect(info.from)
            : undefined,
          scrollIntoView: true,
        });
        view.focus();
      } catch (e) { /* best-effort */ }
    }

    function save() {
      if (!editor || openPath == null) return;
      saveBtn.disabled = true;
      fetch(api("/files/write?path=" + encodeURIComponent(openPath)), {
        method: "POST", credentials: "same-origin", body: editor.getDoc(),
      })
        .then(function (r) { if (!r.ok) return r.text().then(function (t) { throw new Error(t || ("HTTP " + r.status)); }); })
        .then(function () { setDirty(false); })
        .catch(function (err) { currentEl.textContent = "save failed: " + err.message; saveBtn.disabled = false; });
    }

    saveBtn.addEventListener("click", save);
    // Ctrl/Cmd-S saves when the editor has focus.
    cmHost.addEventListener("keydown", function (e) {
      if ((e.ctrlKey || e.metaKey) && (e.key === "s" || e.key === "S")) { e.preventDefault(); if (dirty) save(); }
    });

    // ---- Search: filename find | content grep ------------------------------
    var mode = "name"; // or "grep"
    var searchTimer = null;

    function showTree(show) {
      treeEl.style.display = show ? "" : "none";
      resultsEl.style.display = show ? "none" : "";
    }
    function setMode(m) {
      mode = m;
      modeNameBtn.classList.toggle("active", m === "name");
      modeGrepBtn.classList.toggle("active", m === "grep");
      qEl.placeholder = m === "grep" ? "search file contents…" : "search files…";
      runSearch();
    }
    modeNameBtn.addEventListener("click", function () { setMode("name"); qEl.focus(); });
    modeGrepBtn.addEventListener("click", function () { setMode("grep"); qEl.focus(); });

    function runSearch() {
      var q = qEl.value.trim();
      if (!q) { showTree(true); resultsEl.innerHTML = ""; return; }
      showTree(false);
      resultsEl.innerHTML = '<p class="muted" style="padding:0.5rem">searching…</p>';
      var url = mode === "grep"
        ? api("/files/grep?q=" + encodeURIComponent(q))
        : api("/files/find?q=" + encodeURIComponent(q));
      fetch(url, { credentials: "same-origin" })
        .then(function (r) { if (!r.ok) throw new Error("HTTP " + r.status); return r.json(); })
        .then(function (data) { mode === "grep" ? renderGrep(data) : renderFind(data); })
        .catch(function (err) { resultsEl.innerHTML = '<p class="attention" style="padding:0.5rem">search error: ' + esc(err.message) + "</p>"; });
    }

    function renderFind(data) {
      var matches = data.matches || [];
      if (!matches.length) { resultsEl.innerHTML = '<p class="muted" style="padding:0.5rem">no matching files</p>'; return; }
      resultsEl.innerHTML = "";
      matches.forEach(function (rel) {
        var base = rel.split("/").pop();
        var row = document.createElement("div");
        row.className = "sresult";
        row.innerHTML = fileIcon(base) + '<span class="sresult-path">' + esc(rel) + "</span>";
        row.addEventListener("click", function () { openFile(rel); });
        resultsEl.appendChild(row);
      });
      if (data.truncated) resultsEl.appendChild(truncNote());
    }

    function renderGrep(data) {
      var hits = data.hits || [];
      if (!hits.length) { resultsEl.innerHTML = '<p class="muted" style="padding:0.5rem">no matches</p>'; return; }
      resultsEl.innerHTML = "";
      hits.forEach(function (h) {
        var row = document.createElement("div");
        row.className = "sresult sresult-grep";
        row.innerHTML =
          '<div class="sresult-loc">' + esc(h.path) + '<span class="sresult-ln">:' + h.line + "</span></div>" +
          '<div class="sresult-text">' + esc(h.text) + "</div>";
        row.addEventListener("click", function () { openFile(h.path, h.line); });
        resultsEl.appendChild(row);
      });
      if (data.truncated) resultsEl.appendChild(truncNote());
    }

    function truncNote() {
      var p = document.createElement("p");
      p.className = "muted"; p.style.padding = "0.5rem";
      p.textContent = "…results truncated — narrow your search";
      return p;
    }

    qEl.addEventListener("input", function () {
      clearTimeout(searchTimer);
      searchTimer = setTimeout(runSearch, 220); // debounce
    });

    // Manual refresh button — immediate re-read of the visible tree.
    var refreshBtn = document.getElementById("files-refresh");
    if (refreshBtn) refreshBtn.addEventListener("click", function () { refreshTree(); });

    renderDir(treeEl, ""); // workspace root

    // Auto-refresh: poll the open directories so files Claude creates/deletes
    // appear without a manual collapse+expand. Only polls while the Files tab is
    // actually visible (and pauses during an active filename/grep search, which
    // swaps the tree out). Modelled on the mitm.js flow poller.
    var POLL_MS = 2500;
    setInterval(function () {
      var panel = document.getElementById("tab-files");
      var visible = panel && panel.offsetParent !== null;
      var searching = resultsEl && resultsEl.style.display !== "none";
      if (visible && !searching) refreshTree();
    }, POLL_MS);
  }

  window.startFiles = startFiles;
})();
