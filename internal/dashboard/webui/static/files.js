// Files tab: a lazy directory tree on the left, a CodeMirror editor on the right.
// Reads/writes go straight to the host workspace (which is bind-mounted into the
// container), so edits here are immediately visible to Claude. Pairs with the
// handleFilesTree/Read/Write handlers in dashboard_files.go.
(function () {
  function startFiles(projectId) {
    var root = document.getElementById("files-root");
    if (!root) return;

    root.innerHTML =
      '<div class="files-tree" id="files-tree"></div>' +
      '<div class="files-editor">' +
      '  <div class="files-editor-bar">' +
      '    <span id="files-current" class="muted">select a file</span>' +
      '    <button id="files-save" type="button" disabled>Save</button>' +
      '  </div>' +
      '  <div id="files-cm" class="files-cm"></div>' +
      "</div>";

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

    // Render one directory's entries as a <ul>; directories expand lazily on click.
    function renderDir(parentEl, relPath) {
      fetch(api("/files/tree?path=" + encodeURIComponent(relPath)), { credentials: "same-origin" })
        .then(function (r) { if (!r.ok) throw new Error("HTTP " + r.status); return r.json(); })
        .then(function (data) {
          var ul = document.createElement("ul");
          (data.entries || []).forEach(function (e) {
            var li = document.createElement("li");
            var childRel = relPath ? relPath + "/" + e.name : e.name;
            if (e.dir) {
              li.className = "ftree-dir collapsed";
              li.innerHTML =
                '<span class="ftree-label">' + folderIcon +
                '<span class="ftree-name">' + esc(e.name) + "</span></span>";
              var sub = null;
              li.querySelector(".ftree-label").addEventListener("click", function (ev) {
                ev.stopPropagation();
                if (sub) { sub.remove(); sub = null; li.classList.add("collapsed"); return; }
                li.classList.remove("collapsed");
                sub = document.createElement("div");
                li.appendChild(sub);
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
            ul.appendChild(li);
          });
          parentEl.appendChild(ul);
        })
        .catch(function (err) { parentEl.innerHTML = '<p class="attention">tree error: ' + esc(err.message) + "</p>"; });
    }

    function markActive(rel) {
      treeEl.querySelectorAll(".ftree-file.active").forEach(function (el) { el.classList.remove("active"); });
      var node = treeEl.querySelector('.ftree-file[data-path="' + (window.CSS && CSS.escape ? CSS.escape(rel) : rel) + '"]');
      if (node) node.classList.add("active");
    }

    function openFile(rel) {
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
        })
        .catch(function (err) { cmHost.innerHTML = '<p class="attention" style="padding:1rem">read error: ' + esc(err.message) + "</p>"; });
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

    renderDir(treeEl, ""); // workspace root
  }

  window.startFiles = startFiles;
})();
