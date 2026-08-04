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
    repos.forEach(function (rp) {
      // A per-repo container holds the row + (optional) issues panel, so the
      // panel is a clean block BELOW the row rather than a wrapped flex child
      // (which caused layout bleed). The row keeps its own flex layout intact.
      var item = el("div", { class: "repo-item" });
      var row = el("div", { class: "repo-row" });
      var meta = el("div", { class: "repo-meta" });
      meta.appendChild(el("span", { class: "repo-name" }, rp.name));
      meta.appendChild(el("span", { class: "repo-src" }, rp.url || rp.local_path || ""));
      if (rp.is_private) meta.appendChild(el("span", { class: "repo-badge" }, "private"));
      row.appendChild(meta);
      var actions = el("div", { class: "repo-actions" });
      var create = el("button", { type: "button", class: "btn primary" }, "Create project");
      create.addEventListener("click", function () { openNewProject({ repoId: rp.id, name: rp.name }); });
      // Issues: only for repos with a github URL (need owner/name for gh).
      var ownerName = ghOwnerName(rp.url);
      var issuesPanel = null;
      var issuesBtn = el("button", { type: "button", class: "btn", title: "Browse this repo's GitHub issues" }, "Issues");
      if (!ownerName) issuesBtn.disabled = true;
      issuesBtn.addEventListener("click", function () {
        if (issuesPanel) { issuesPanel.remove(); issuesPanel = null; issuesBtn.classList.remove("on"); return; }
        issuesBtn.classList.add("on");
        issuesPanel = renderIssuesPanel(rp, ownerName);
        item.appendChild(issuesPanel);
      });
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
      actions.appendChild(create); actions.appendChild(issuesBtn); actions.appendChild(refresh); actions.appendChild(del);
      row.appendChild(actions);
      item.appendChild(row);
      reposList.appendChild(item);
    });
  }

  // ---- issues (browse / create / spawn a project off a GitHub issue) ------
  // Derive "owner/name" from a github URL; null if it isn't a github repo URL.
  function ghOwnerName(url) {
    if (!url) return null;
    var m = String(url).match(/github\.com[:/]+([^/]+)\/([^/]+?)(?:\.git)?\/?$/);
    return m ? (m[1] + "/" + m[2]) : null;
  }

  // Short "3 days ago"-style relative date from an ISO timestamp.
  function relDate(iso) {
    if (!iso) return "";
    var then = new Date(iso).getTime();
    if (isNaN(then)) return "";
    var s = Math.max(0, (Date.now() - then) / 1000);
    var units = [["y", 31536000], ["mo", 2592000], ["d", 86400], ["h", 3600], ["m", 60]];
    for (var i = 0; i < units.length; i++) {
      var n = Math.floor(s / units[i][1]);
      if (n >= 1) return n + units[i][0] + " ago";
    }
    return "just now";
  }

  // renderIssuesPanel returns an inline panel (appended under a repo row) with a
  // "New issue" button + the repo's open issues (number, title, author, date),
  // each with a "Spawn project" button that opens the spawn modal.
  function renderIssuesPanel(rp, ownerName) {
    var panel = el("div", { class: "issues-panel" });
    var head = el("div", { class: "issues-head" });
    head.appendChild(el("span", { class: "muted" }, ownerName));
    var newBtn = el("button", { type: "button", class: "btn" }, "+ New issue");
    newBtn.addEventListener("click", function () { openNewIssue(rp, ownerName, function () { load(); }); });
    head.appendChild(newBtn);
    panel.appendChild(head);
    var listWrap = el("div", { class: "issues-list" });
    panel.appendChild(listWrap);

    function load() {
      listWrap.innerHTML = "";
      listWrap.appendChild(el("div", { class: "ta-loading" }, "loading issues…"));
      jfetch("/gh/issues?repo=" + encodeURIComponent(ownerName)).then(function (d) {
        listWrap.innerHTML = "";
        if (!d || !d.available) {
          listWrap.appendChild(el("div", { class: "muted" }, "couldn't load issues (" + esc((d && d.reason) || "gh error") + ")"));
          return;
        }
        var issues = d.issues || [];
        if (!issues.length) { listWrap.appendChild(el("div", { class: "muted" }, "no open issues")); return; }
        issues.forEach(function (iss) {
          var it = el("div", { class: "issue-row" });
          var meta = el("div", { class: "issue-meta" });
          var titleLine = el("div", { class: "issue-titleline" });
          titleLine.appendChild(el("span", { class: "issue-num" }, "#" + iss.number));
          titleLine.appendChild(el("span", { class: "issue-title", title: iss.title }, iss.title));
          meta.appendChild(titleLine);
          var by = (iss.author && iss.author.login) ? "@" + iss.author.login : "";
          var sub = [by, relDate(iss.createdAt)].filter(Boolean).join(" · ");
          if (sub) meta.appendChild(el("div", { class: "issue-sub muted" }, sub));
          it.appendChild(meta);
          var spawn = el("button", { type: "button", class: "btn primary issue-spawn" }, "Spawn");
          spawn.addEventListener("click", function () { openSpawnModal(rp, ownerName, iss); });
          it.appendChild(spawn);
          listWrap.appendChild(it);
        });
      }).catch(function (e) {
        listWrap.innerHTML = "";
        listWrap.appendChild(el("div", { class: "attention" }, "issues error: " + esc(e.message)));
      });
    }
    load();
    return panel;
  }

  // openNewIssue: a modal to file a new issue on the repo (title + body ->
  // gh issue create), then onCreated() refreshes the list.
  function openNewIssue(rp, ownerName, onCreated) {
    var f = el("form", { class: "sc-form" });
    f.innerHTML =
      '<label>Title <input name="title" type="text" placeholder="issue title" autocomplete="off"></label>' +
      '<label>Body <textarea name="body" rows="5" placeholder="describe the issue (optional)"></textarea></label>' +
      '<div class="form-actions"><button type="submit" class="btn primary">Create issue</button>' +
      '<span class="form-status"></span></div>';
    f.addEventListener("submit", function (e) {
      e.preventDefault();
      var title = f.title.value.trim();
      if (!title) { status(f, "title is required", true); return; }
      status(f, "creating…", false);
      jfetch("/gh/issues/create", { method: "POST", body: { repo: ownerName, title: title, body: f.body.value } })
        .then(function (res) { closeModal(); if (onCreated) onCreated(); })
        .catch(function (err) { status(f, err.message, true); });
    });
    openModal("New issue · " + ownerName, f);
  }

  // openSpawnModal: confirm + spawn a project off an issue, with an Advanced
  // section to clone additional repos alongside the issue's repo (reuses the
  // multi-repo rows from the New-project modal).
  function openSpawnModal(rp, ownerName, iss) {
    var state = { ghRepos: ghReposCache || [], loaded: !!ghReposCache };
    var form = el("form", { class: "sc-form" });

    var summary = el("div", { class: "spawn-summary" });
    summary.appendChild(el("div", {}, "Spawn a project to work on:"));
    var issLine = el("div", { class: "issue-titleline" });
    issLine.appendChild(el("span", { class: "issue-num" }, "#" + iss.number));
    issLine.appendChild(el("span", { class: "issue-title", title: iss.title }, iss.title));
    summary.appendChild(issLine);
    summary.appendChild(el("div", { class: "muted", style: "font-size:0.78rem" },
      "clones " + ownerName + " on a new branch, writes ISSUE.md, and pre-types a prompt into Claude."));
    form.appendChild(summary);

    // Advanced: add extra repos alongside the issue's repo.
    var adv = el("details", { class: "spawn-advanced" });
    adv.appendChild(el("summary", {}, "Advanced — add another repo"));
    var extraRows = el("div", { class: "repo-rows" });
    adv.appendChild(extraRows);
    var liveExtra = [];
    var addExtra = el("button", { type: "button", class: "btn add-row" }, "+ add another repo");
    addExtra.addEventListener("click", function () { var r = repoRow(state, true); extraRows.appendChild(r); liveExtra.push(r); });
    adv.appendChild(addExtra);
    form.appendChild(adv);
    // Backfill the extra-repo typeaheads once gh repos load.
    loadGhRepos().then(function () { state.ghRepos = ghReposCache || []; state.loaded = true; liveExtra.forEach(function (r) { if (r._refreshRepos) r._refreshRepos(); }); });

    var actions = el("div", { class: "form-actions" });
    var go = el("button", { type: "submit", class: "btn primary" }, "Spawn project");
    actions.appendChild(go);
    actions.appendChild(el("span", { class: "form-status" }));
    form.appendChild(actions);

    form.addEventListener("submit", function (e) {
      e.preventDefault();
      go.disabled = true; status(form, "spawning…", false);
      // repos: the issue repo first, then any Advanced extras (skip empty rows).
      var specs = [{ repoId: rp.id }];
      liveExtra.forEach(function (r) {
        var s = toSpec(r._value(), state.ghRepos || []);
        if (s) specs.push(s);
      });
      var body = {
        mode: "clone", repos: specs, name: rp.name + "-" + iss.number,
        issue: { number: iss.number, title: iss.title, body: iss.body || "", url: iss.url, repo: ownerName },
      };
      jfetch("/projects/create", { method: "POST", body: body }).then(function (res) {
        var id = res.id, prompt = res.issue_prompt || "";
        jfetch("/p/" + id + "/start", { method: "POST" }).catch(function () {});
        if (prompt) jfetch("/p/" + id + "/populate-prompt", { method: "POST", body: { prompt: prompt } }).catch(function () {});
        window.location.href = "/p/" + id + "/";
      }).catch(function (err) { go.disabled = false; status(form, err.message, true); });
    });
    openModal("Spawn project · #" + iss.number, form);
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
