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
  var reposCache = []; // last-loaded repos, so the search box filters without refetching

  function loadRepos() {
    if (!reposList) return;
    jfetch("/repos").then(function (data) {
      reposCache = data.repos || [];
      renderRepos();
    }).catch(function (e) { reposList.innerHTML = '<p class="attention">repos error: ' + esc(e.message) + "</p>"; });
  }

  // renderRepos paints reposCache, filtered by the search box (name/url/path,
  // case-insensitive). Called on load and on every keystroke in #repos-search.
  function renderRepos() {
    if (!reposList) return;
    if (!reposCache.length) {
      reposList.innerHTML = '<p class="muted">No repos cached yet. Add one to spin projects off it quickly.</p>';
      return;
    }
    var searchEl = document.getElementById("repos-search");
    var q = (searchEl && searchEl.value.trim().toLowerCase()) || "";
    var repos = !q ? reposCache : reposCache.filter(function (rp) {
      return (rp.name || "").toLowerCase().indexOf(q) >= 0 ||
             (rp.url || "").toLowerCase().indexOf(q) >= 0 ||
             (rp.local_path || "").toLowerCase().indexOf(q) >= 0;
    });
    if (!repos.length) {
      reposList.innerHTML = '<p class="muted">No repos match “' + esc(q) + '”.</p>';
      return;
    }
    reposList.innerHTML = "";
    {
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
    }
  }

  // ---- add repo -----------------------------------------------------------
  function openAddRepo() {
    var f = el("form", { class: "sc-form" });

    // Repo picker: a typeahead seeded from the user's GitHub repos (via gh), so
    // you can search-and-pick instead of pasting a URL. Free-text still works —
    // paste a URL or a local /path for anything gh doesn't list. Mirrors the
    // New-project modal's repo picker.
    var repoTa = typeahead({
      class: "repo-input",
      placeholder: "search your GitHub repos, or paste a URL / local path",
      items: [],
      onPick: function (val) {
        // A picked gh repo owner/name is cloned with auth — default Private on.
        var g = (ghReposCache || []).filter(function (r) { return r.nameWithOwner === val; })[0];
        if (g) f.priv.checked = !!g.isPrivate;
      },
    });

    var picker = el("label", {}, "Repository");
    picker.appendChild(repoTa);
    f.appendChild(picker);

    // Spinner while `gh repo list` runs (can take a couple seconds). Reuses the
    // .ta-loading spinner the New-project modal uses; hidden once repos load.
    var loading = el("div", { class: "ta-loading" }, "loading your GitHub repos…");
    if (ghReposCache) loading.style.display = "none"; // already cached from a prior open
    f.appendChild(loading);

    var rest = el("div");
    rest.innerHTML =
      '<label>Name (optional) <input name="name" type="text" placeholder="defaults from the repo" autocomplete="off"></label>' +
      '<label class="row"><input name="priv" type="checkbox"> Private (clone with your host git/gh auth)</label>' +
      '<div class="form-actions"><button type="submit" class="btn primary">Add</button>' +
      '<span class="form-status"></span></div>';
    f.appendChild(rest);

    // Backfill the picker once the user's gh repos load (async). Non-blocking:
    // the form is usable immediately for a pasted URL even if gh is slow/absent.
    loadGhRepos().then(function () {
      repoTa.setItems(repoItems(ghReposCache || []));
      loading.style.display = "none";
      if (!(ghReposCache || []).length) {
        // gh unavailable / not authed / no repos — say so, but the URL/path path
        // still works.
        loading.className = "muted";
        loading.textContent = "no GitHub repos found (paste a URL or local path instead)";
        loading.style.display = "";
      }
    });

    f.addEventListener("submit", function (e) {
      e.preventDefault();
      var val = repoTa.value().trim();
      if (!val) { status(f, "pick a repo, or paste a URL / local path", true); return; }
      status(f, "cloning cache mirror…", false);
      // A gh pick is "owner/name" (no scheme, not an absolute path). Turn it into
      // a github URL; a real URL or a local /path is passed through as-is.
      var isLocal = val.indexOf("://") < 0 && val.charAt(0) === "/";
      var isGh = !isLocal && val.indexOf("://") < 0 && val.indexOf("/") > 0;
      var body = { name: f.name.value.trim(), isPrivate: f.priv.checked };
      if (isLocal) body.localPath = val;
      else body.url = isGh ? ("https://github.com/" + val) : val;
      jfetch("/repos", { method: "POST", body: body })
        .then(function () { closeModal(); loadRepos(); })
        .catch(function (err) { status(f, err.message, true); });
    });
    openModal("Add repository", f);
    setTimeout(function () { repoTa.focus(); }, 0);
  }

  // ---- typeahead widget ---------------------------------------------------
  // A text input with a filtered dropdown list rendered below it. items is an
  // array of {value, label, hint}. onPick(value) fires on selection. The input
  // stays free-text (any typed value is accepted), so it works for both a fixed
  // list (repos) and an async-loaded one (branches).
  function typeahead(opts) {
    var wrap = el("div", { class: "ta" });
    var input = el("input", { type: "text", class: opts.class || "ta-input", placeholder: opts.placeholder || "", autocomplete: "off" });
    var menu = el("div", { class: "ta-menu", hidden: "" });
    wrap.appendChild(input); wrap.appendChild(menu);
    var items = opts.items || [];
    var active = -1;

    function render() {
      var q = input.value.trim().toLowerCase();
      var matches = items.filter(function (it) {
        return !q || it.value.toLowerCase().indexOf(q) >= 0 || (it.label || "").toLowerCase().indexOf(q) >= 0;
      }).slice(0, 30);
      if (!matches.length) { menu.setAttribute("hidden", ""); menu.innerHTML = ""; return; }
      menu.innerHTML = "";
      matches.forEach(function (it, i) {
        var row = el("div", { class: "ta-opt" + (i === active ? " active" : "") });
        row.appendChild(el("span", { class: "ta-val" }, it.label || it.value));
        if (it.hint) row.appendChild(el("span", { class: "ta-hint" }, it.hint));
        row.addEventListener("mousedown", function (e) { e.preventDefault(); choose(it); });
        menu.appendChild(row);
      });
      menu.removeAttribute("hidden");
    }
    function choose(it) {
      input.value = it.value;
      menu.setAttribute("hidden", ""); active = -1;
      if (opts.onPick) opts.onPick(it.value);
    }
    input.addEventListener("input", function () { active = -1; render(); if (opts.onInput) opts.onInput(input.value.trim()); });
    input.addEventListener("focus", render);
    input.addEventListener("blur", function () { setTimeout(function () { menu.setAttribute("hidden", ""); }, 120); });
    input.addEventListener("keydown", function (e) {
      var vis = menu.querySelectorAll(".ta-opt");
      if (e.key === "ArrowDown") { e.preventDefault(); active = Math.min(active + 1, vis.length - 1); render2(vis); }
      else if (e.key === "ArrowUp") { e.preventDefault(); active = Math.max(active - 1, 0); render2(vis); }
      else if (e.key === "Enter" && active >= 0 && vis[active]) { e.preventDefault(); vis[active].dispatchEvent(new MouseEvent("mousedown")); }
      else if (e.key === "Escape") { menu.setAttribute("hidden", ""); }
    });
    function render2(vis) { vis.forEach(function (v, i) { v.classList.toggle("active", i === active); }); }

    wrap.setItems = function (list) { items = list || []; if (document.activeElement === input) render(); };
    wrap.value = function () { return input.value.trim(); };
    wrap.focus = function () { input.focus(); };
    return wrap;
  }

  // ---- new project (multi-repo) ------------------------------------------
  // Branch cache per "owner/name" so re-selecting a repo doesn't refetch.
  var branchCache = {};
  function fetchBranches(ownerName) {
    if (branchCache[ownerName]) return Promise.resolve(branchCache[ownerName]);
    return jfetch("/gh/branches?repo=" + encodeURIComponent(ownerName))
      .then(function (d) { var b = (d && d.available) ? (d.branches || []) : []; branchCache[ownerName] = b; return b; })
      .catch(function () { return []; });
  }

  // One repo row: a repo typeahead + a branch typeahead + a remove button.
  // ghRepos is a live-updating array (starts empty, filled when /gh/repos loads).
  function repoRow(state, removable) {
    var row = el("div", { class: "repo-input-row" });
    var repoTa = typeahead({
      class: "repo-input", placeholder: "pick a repo, or paste a URL / local path",
      items: repoItems(state.ghRepos),
      onPick: function (val) { loadBranchesFor(val); },
      onInput: function (val) { loadBranchesFor(val); },
    });
    var branchTa = typeahead({ class: "branch-input", placeholder: "branch", items: [] });
    var rm = el("button", { type: "button", class: "btn row-rm", title: "Remove" }, "−");
    rm.addEventListener("click", function () { row.remove(); });
    if (!removable) rm.style.visibility = "hidden";
    row.appendChild(repoTa); row.appendChild(branchTa); row.appendChild(rm);

    // If the typed/picked value is a gh "owner/name", load its branches.
    var branchTimer = null;
    function loadBranchesFor(val) {
      var gh = state.ghRepos.find(function (g) { return g.nameWithOwner === val || g.url === val; });
      var ownerName = gh ? gh.nameWithOwner : (/^[\w.-]+\/[\w.-]+$/.test(val) ? val : null);
      if (!ownerName) { branchTa.setItems([]); return; }
      clearTimeout(branchTimer);
      branchTimer = setTimeout(function () {
        fetchBranches(ownerName).then(function (bs) {
          branchTa.setItems(bs.map(function (b) { return { value: b, label: b }; }));
        });
      }, 200);
    }

    row._repoTa = repoTa; row._branchTa = branchTa;
    row._value = function () { return { text: repoTa.value(), branch: branchTa.value() }; };
    row._refreshRepos = function () { repoTa.setItems(repoItems(state.ghRepos)); };
    return row;
  }
  function repoItems(ghRepos) {
    return ghRepos.map(function (g) {
      return { value: g.nameWithOwner, label: g.nameWithOwner, hint: g.isPrivate ? "private" : "" };
    });
  }

  // Turn a row's value into a repo spec. Match a gh repo (owner/name or url) →
  // url; a local path → localPath; else url.
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
    // Open IMMEDIATELY; repos load async and backfill the typeaheads.
    var state = { ghRepos: [], loaded: false };
    var wrap = el("div");
    wrap.innerHTML =
      '<div class="mode-tabs">' +
      '  <button type="button" data-mode="clone" class="active">From repo(s)</button>' +
      '  <button type="button" data-mode="new">Blank dir</button>' +
      '  <button type="button" data-mode="existing">Existing dir</button>' +
      '</div><div id="mode-body"></div>';
    var body = wrap.querySelector("#mode-body");
    var tabs = wrap.querySelectorAll(".mode-tabs button");
    var liveRows = []; // repo rows currently rendered, so we can backfill them

    function cloneForm() {
      var form = el("form", { class: "sc-form" });
      var loading = el("div", { class: "ta-loading" }, "loading your repos…");
      if (state.loaded) loading.style.display = "none";
      form.appendChild(loading);
      var rows = el("div", { class: "repo-rows" });
      form.appendChild(rows);
      liveRows = [];
      function addRow(removable) { var r = repoRow(state, removable); rows.appendChild(r); liveRows.push(r); return r; }
      addRow(false);
      if (preset && preset.repoId) form._presetRepoId = preset.repoId;

      var add = el("button", { type: "button", class: "btn add-row" }, "+ add another repo");
      add.addEventListener("click", function () { addRow(true); });
      form.appendChild(add);
      var nameLabel = el("label", null, "Project name (optional)");
      var name = el("input", { type: "text", placeholder: "defaults from the repo", autocomplete: "off" });
      nameLabel.appendChild(name); form.appendChild(nameLabel);
      var actions = el("div", { class: "form-actions" });
      actions.appendChild(el("button", { type: "submit", class: "btn primary" }, "Create & open"));
      actions.appendChild(el("span", { class: "form-status" }));
      form.appendChild(actions);

      form.addEventListener("submit", function (e) {
        e.preventDefault();
        var specs = [];
        if (form._presetRepoId) specs.push({ repoId: form._presetRepoId });
        rows.querySelectorAll(".repo-input-row").forEach(function (r) {
          var s = toSpec(r._value(), state.ghRepos);
          if (s) specs.push(s);
        });
        if (!specs.length) { status(form, "add at least one repo", true); return; }
        submitCreate(form, { mode: "clone", name: name.value.trim(), repos: specs });
      });
      form._loadingEl = loading;
      return form;
    }
    function newForm() {
      var form = el("form", { class: "sc-form" });
      form.innerHTML =
        '<label>Name <input name="name" type="text" placeholder="my-project" autocomplete="off"></label>' +
        '<div class="form-actions"><button type="submit" class="btn primary">Create &amp; open</button><span class="form-status"></span></div>';
      form.addEventListener("submit", function (e) { e.preventDefault(); submitCreate(form, { mode: "new", name: form.name.value.trim() }); });
      return form;
    }
    function existingForm() {
      var form = el("form", { class: "sc-form" });
      form.innerHTML =
        '<label>Absolute path <input name="path" type="text" placeholder="/Users/you/code/project" autocomplete="off"></label>' +
        '<div class="form-actions"><button type="submit" class="btn primary">Register &amp; open</button><span class="form-status"></span></div>';
      form.addEventListener("submit", function (e) { e.preventDefault(); submitCreate(form, { mode: "existing", path: form.path.value.trim() }); });
      return form;
    }
    var currentForm = null;
    function setMode(m) {
      tabs.forEach(function (t) { t.classList.toggle("active", t.dataset.mode === m); });
      body.innerHTML = ""; liveRows = [];
      currentForm = m === "new" ? newForm() : m === "existing" ? existingForm() : cloneForm();
      body.appendChild(currentForm);
    }
    tabs.forEach(function (t) { t.addEventListener("click", function () { setMode(t.dataset.mode); }); });
    setMode("clone");
    openModal("New project", wrap);

    // Async: fill repos in when they arrive, backfilling any live rows.
    loadGhRepos().then(function (ghRepos) {
      state.ghRepos = ghRepos; state.loaded = true;
      if (currentForm && currentForm._loadingEl) currentForm._loadingEl.style.display = "none";
      liveRows.forEach(function (r) { if (r._refreshRepos) r._refreshRepos(); });
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

  // Live-filter the repos list as you type (renders from the cache, no refetch).
  var reposSearch = document.getElementById("repos-search");
  if (reposSearch) reposSearch.addEventListener("input", renderRepos);

  loadRepos();
})();
