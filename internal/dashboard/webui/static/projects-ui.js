// Landing-page project/repo management: sidebar section switching, the repos
// list + Add-repo form, and the New-project modal (dynamic multi-repo rows,
// each a gh-known-repo picker or a free URL/path; or a blank dir). Pairs with
// /repos, /gh/repos and /projects/create. Vanilla JS, dark console theme.
(function () {
  var reposList = document.getElementById("repos-list");
  var modal = document.getElementById("modal");
  var modalTitle = document.getElementById("modal-title");
  var modalBody = document.getElementById("modal-body");
  if (!modal) return;

  function esc(s) {
    return String(s == null ? "" : s).replace(/&/g, "&amp;").replace(/</g, "&lt;")
      .replace(/>/g, "&gt;").replace(/"/g, "&quot;");
  }
  function jfetch(url, opts) {
    opts = opts || {};
    opts.credentials = "same-origin";
    if (opts.body && typeof opts.body !== "string") {
      opts.headers = { "Content-Type": "application/json" };
      opts.body = JSON.stringify(opts.body);
    }
    return fetch(url, opts).then(function (r) {
      if (!r.ok) return r.text().then(function (t) { throw new Error(t || ("HTTP " + r.status)); });
      return r.status === 204 ? {} : r.json();
    });
  }
  function el(tag, attrs, text) {
    var e = document.createElement(tag);
    if (attrs) Object.keys(attrs).forEach(function (k) { e.setAttribute(k, attrs[k]); });
    if (text != null) e.textContent = text;
    return e;
  }
  function status(node, msg, isErr) {
    var s = node.querySelector(".form-status");
    if (s) { s.textContent = msg; s.className = "form-status" + (isErr ? " error" : ""); }
  }

  // ---- sidebar section switching -----------------------------------------
  var navItems = document.querySelectorAll(".nav-item[data-section]");
  var titleEl = document.getElementById("section-title");
  function showSection(name) {
    navItems.forEach(function (b) { b.classList.toggle("active", b.dataset.section === name); });
    document.querySelectorAll(".section").forEach(function (s) {
      var on = s.id === "section-" + name;
      s.classList.toggle("active", on);
      if (on) s.removeAttribute("hidden"); else s.setAttribute("hidden", "");
    });
    if (titleEl) titleEl.textContent = name === "repos" ? "Repos" : "Projects";
    if (name === "repos") loadRepos();
  }
  navItems.forEach(function (b) {
    b.addEventListener("click", function () { showSection(b.dataset.section); });
  });

  // ---- modal --------------------------------------------------------------
  function openModal(title, node) {
    modalTitle.textContent = title;
    modalBody.innerHTML = "";
    modalBody.appendChild(node);
    modal.removeAttribute("hidden");
  }
  function closeModal() { modal.setAttribute("hidden", ""); modalBody.innerHTML = ""; }
  document.getElementById("modal-close").addEventListener("click", closeModal);
  modal.addEventListener("click", function (e) { if (e.target === modal) closeModal(); });

  // ---- gh repos (cached once) --------------------------------------------
  var ghReposCache = null;
  function loadGhRepos() {
    if (ghReposCache) return Promise.resolve(ghReposCache);
    return jfetch("/gh/repos").then(function (d) {
      ghReposCache = d && d.available ? (d.repos || []) : [];
      return ghReposCache;
    }).catch(function () { ghReposCache = []; return ghReposCache; });
  }

  // ---- repos list ---------------------------------------------------------
  function loadRepos() {
    if (!reposList) return;
    jfetch("/repos").then(function (data) {
      var repos = data.repos || [];
      if (!repos.length) { reposList.innerHTML = '<p class="muted">No repos cached yet. Add one to spin projects off it quickly.</p>'; return; }
      reposList.innerHTML = "";
      repos.forEach(function (rp) {
        var row = el("div", { class: "repo-row" });
        var meta = el("div", { class: "repo-meta" });
        meta.appendChild(el("span", { class: "repo-name" }, rp.name));
        meta.appendChild(el("span", { class: "repo-src" }, rp.url || rp.local_path || ""));
        if (rp.is_private) meta.appendChild(el("span", { class: "repo-badge" }, "private"));
        row.appendChild(meta);
        var actions = el("div", { class: "repo-actions" });
        var create = el("button", { type: "button", class: "btn primary" }, "Create project");
        create.addEventListener("click", function () { openNewProject({ repoId: rp.id, name: rp.name }); });
        var refresh = el("button", { type: "button", class: "btn", title: "Refresh cache" }, "⟳");
        refresh.addEventListener("click", function () {
          refresh.disabled = true;
          jfetch("/repos/" + encodeURIComponent(rp.id) + "/fetch", { method: "POST" })
            .then(loadRepos).catch(function (e) { alert("refresh failed: " + e.message); refresh.disabled = false; });
        });
        var del = el("button", { type: "button", class: "btn", title: "Remove repo" }, "✕");
        del.addEventListener("click", function () {
          if (!confirm("Remove '" + rp.name + "'? Spun-off projects are kept.")) return;
          jfetch("/repos/" + encodeURIComponent(rp.id), { method: "DELETE" }).then(loadRepos)
            .catch(function (e) { alert("remove failed: " + e.message); });
        });
        actions.appendChild(create); actions.appendChild(refresh); actions.appendChild(del);
        row.appendChild(actions);
        reposList.appendChild(row);
      });
    }).catch(function (e) { reposList.innerHTML = '<p class="attention">repos error: ' + esc(e.message) + "</p>"; });
  }

  // ---- add repo -----------------------------------------------------------
  function openAddRepo() {
    var f = el("form", { class: "sc-form" });
    f.innerHTML =
      '<label>Repository URL <input name="url" type="text" placeholder="https://github.com/org/repo (or a local path)" autocomplete="off"></label>' +
      '<label>Name (optional) <input name="name" type="text" placeholder="defaults from the URL" autocomplete="off"></label>' +
      '<label class="row"><input name="priv" type="checkbox"> Private (clone with your host git/gh auth)</label>' +
      '<div class="form-actions"><button type="submit" class="btn primary">Add</button>' +
      '<span class="form-status"></span></div>';
    f.addEventListener("submit", function (e) {
      e.preventDefault();
      var url = f.url.value.trim();
      if (!url) { status(f, "a URL or local path is required", true); return; }
      status(f, "cloning cache mirror…", false);
      var isLocal = url.indexOf("://") < 0 && url.charAt(0) === "/";
      var body = { name: f.name.value.trim(), isPrivate: f.priv.checked };
      body[isLocal ? "localPath" : "url"] = url;
      jfetch("/repos", { method: "POST", body: body })
        .then(function () { closeModal(); loadRepos(); })
        .catch(function (err) { status(f, err.message, true); });
    });
    openModal("Add repository", f);
  }

  // ---- new project (multi-repo) ------------------------------------------
  // One repo row: a combined datalist input (gh-known repos as suggestions, but
  // any URL/path is accepted) + an optional branch + a remove button.
  function repoRow(ghRepos, removable) {
    var row = el("div", { class: "repo-input-row" });
    var input = el("input", {
      type: "text", class: "repo-input", list: "gh-repos-dl",
      placeholder: "pick a repo, or paste a URL / local path", autocomplete: "off",
    });
    var branch = el("input", { type: "text", class: "branch-input", placeholder: "branch", autocomplete: "off" });
    var rm = el("button", { type: "button", class: "btn row-rm", title: "Remove" }, "−");
    rm.addEventListener("click", function () { row.remove(); });
    if (!removable) rm.style.visibility = "hidden";
    row.appendChild(input); row.appendChild(branch); row.appendChild(rm);
    row._value = function () { return { text: input.value.trim(), branch: branch.value.trim() }; };
    return row;
  }

  // Turn a row's free-text value into a repo spec. If it matches a gh repo's
  // "owner/name" or url, send a url; if it looks like a local path, localPath;
  // otherwise treat as a url.
  function toSpec(v, ghRepos) {
    if (!v.text) return null;
    var spec = { branch: v.branch };
    var gh = ghRepos.find(function (g) { return g.nameWithOwner === v.text || g.url === v.text; });
    if (gh) { spec.url = gh.url; return spec; }
    if (v.text.indexOf("://") < 0 && v.text.charAt(0) === "/") spec.localPath = v.text;
    else spec.url = v.text;
    return spec;
  }

  function openNewProject(preset) {
    loadGhRepos().then(function (ghRepos) {
      var wrap = el("div");
      wrap.innerHTML =
        '<div class="mode-tabs">' +
        '  <button type="button" data-mode="clone" class="active">From repo(s)</button>' +
        '  <button type="button" data-mode="new">Blank dir</button>' +
        '  <button type="button" data-mode="existing">Existing dir</button>' +
        '</div>' +
        '<datalist id="gh-repos-dl">' +
        ghRepos.map(function (g) { return '<option value="' + esc(g.nameWithOwner) + '">' + (g.isPrivate ? "private" : "public") + "</option>"; }).join("") +
        '</datalist>' +
        '<div id="mode-body"></div>';
      var body = wrap.querySelector("#mode-body");
      var tabs = wrap.querySelectorAll(".mode-tabs button");

      function cloneForm() {
        var form = el("form", { class: "sc-form" });
        var rows = el("div", { class: "repo-rows" });
        form.appendChild(rows);
        rows.appendChild(repoRow(ghRepos, false));
        if (preset && preset.repoId) {
          // Preset from a cached repo's "Create project": use its id directly.
          form._presetRepoId = preset.repoId;
        }
        var add = el("button", { type: "button", class: "btn add-row" }, "+ add another repo");
        add.addEventListener("click", function () { rows.appendChild(repoRow(ghRepos, true)); });
        form.appendChild(add);
        var nameLabel = el("label", null, "Project name (optional)");
        var name = el("input", { type: "text", placeholder: "defaults from the repo", autocomplete: "off" });
        nameLabel.appendChild(name);
        form.appendChild(nameLabel);
        var actions = el("div", { class: "form-actions" });
        var submit = el("button", { type: "submit", class: "btn primary" }, "Create & open");
        actions.appendChild(submit); actions.appendChild(el("span", { class: "form-status" }));
        form.appendChild(actions);

        form.addEventListener("submit", function (e) {
          e.preventDefault();
          var specs = [];
          if (form._presetRepoId) specs.push({ repoId: form._presetRepoId });
          rows.querySelectorAll(".repo-input-row").forEach(function (r) {
            var s = toSpec(r._value(), ghRepos);
            if (s) specs.push(s);
          });
          if (!specs.length) { status(form, "add at least one repo", true); return; }
          submitCreate(form, { mode: "clone", name: name.value.trim(), repos: specs });
        });
        return form;
      }
      function newForm() {
        var form = el("form", { class: "sc-form" });
        form.innerHTML =
          '<label>Name <input name="name" type="text" placeholder="my-project" autocomplete="off"></label>' +
          '<div class="form-actions"><button type="submit" class="btn primary">Create &amp; open</button><span class="form-status"></span></div>';
        form.addEventListener("submit", function (e) {
          e.preventDefault();
          submitCreate(form, { mode: "new", name: form.name.value.trim() });
        });
        return form;
      }
      function existingForm() {
        var form = el("form", { class: "sc-form" });
        form.innerHTML =
          '<label>Absolute path <input name="path" type="text" placeholder="/Users/you/code/project" autocomplete="off"></label>' +
          '<div class="form-actions"><button type="submit" class="btn primary">Register &amp; open</button><span class="form-status"></span></div>';
        form.addEventListener("submit", function (e) {
          e.preventDefault();
          submitCreate(form, { mode: "existing", path: form.path.value.trim() });
        });
        return form;
      }
      function setMode(m) {
        tabs.forEach(function (t) { t.classList.toggle("active", t.dataset.mode === m); });
        body.innerHTML = "";
        body.appendChild(m === "new" ? newForm() : m === "existing" ? existingForm() : cloneForm());
      }
      tabs.forEach(function (t) { t.addEventListener("click", function () { setMode(t.dataset.mode); }); });
      setMode("clone");
      openModal("New project", wrap);
    });
  }

  function submitCreate(form, body) {
    status(form, "creating…", false);
    jfetch("/projects/create", { method: "POST", body: body })
      .then(function (res) { window.location.href = "/p/" + res.id + "/"; })
      .catch(function (err) { status(form, err.message, true); });
  }

  // ---- wire up ------------------------------------------------------------
  var addRepoBtn = document.getElementById("add-repo");
  if (addRepoBtn) addRepoBtn.addEventListener("click", openAddRepo);
  var np = document.getElementById("new-project");
  if (np) np.addEventListener("click", function () { openNewProject(null); });

  loadRepos();
})();
