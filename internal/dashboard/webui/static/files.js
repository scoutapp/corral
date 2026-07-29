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
              li.innerHTML = '<span class="ftree-label">▸ ' + esc(e.name) + "</span>";
              var sub = null;
              li.querySelector(".ftree-label").addEventListener("click", function (ev) {
                ev.stopPropagation();
                if (sub) { sub.remove(); sub = null; li.classList.add("collapsed"); li.querySelector(".ftree-label").textContent = "▸ " + e.name; return; }
                li.classList.remove("collapsed");
                li.querySelector(".ftree-label").textContent = "▾ " + e.name;
                sub = document.createElement("div");
                li.appendChild(sub);
                renderDir(sub, childRel);
              });
            } else {
              li.className = "ftree-file";
              li.innerHTML = '<span class="ftree-label">' + esc(e.name) + "</span>";
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

    function openFile(rel) {
      if (dirty && !window.confirm("Discard unsaved changes to " + openPath + "?")) return;
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
