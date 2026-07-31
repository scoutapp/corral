// Landing-page project/repo management: the repos list + Add-repo form and the
// New-project modal (existing dir / new dir / clone a repo). Pairs with the
// /repos and /projects/create endpoints. Vanilla JS, same dark console theme.
(function () {
  var reposList = document.getElementById("repos-list");
  var modal = document.getElementById("modal");
  var modalTitle = document.getElementById("modal-title");
  var modalBody = document.getElementById("modal-body");
  if (!reposList) return;

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

  // ---- repos list ---------------------------------------------------------
  function loadRepos() {
    jfetch("/repos").then(function (data) {
      var repos = data.repos || [];
      if (!repos.length) { reposList.innerHTML = '<p class="muted">No repos yet. Add one to create projects from it.</p>'; return; }
      reposList.innerHTML = "";
      repos.forEach(function (rp) {
        var row = el("div", { class: "repo-row" });
        var meta = el("div", { class: "repo-meta" });
        meta.appendChild(el("span", { class: "repo-name" }, rp.name));
        meta.appendChild(el("span", { class: "repo-src" }, rp.url || rp.local_path || ""));
        if (rp.is_private) meta.appendChild(el("span", { class: "repo-badge" }, "private"));
        row.appendChild(meta);
        var actions = el("div", { class: "repo-actions" });
        var create = el("button", { type: "button", class: "repo-btn primary" }, "Create project");
        create.addEventListener("click", function () { openCloneForm(rp); });
        var refresh = el("button", { type: "button", class: "repo-btn", title: "Refresh cache" }, "⟳");
        refresh.addEventListener("click", function () {
          refresh.disabled = true;
          jfetch("/repos/" + encodeURIComponent(rp.id) + "/fetch", { method: "POST" })
            .then(loadRepos).catch(function (e) { alert("refresh failed: " + e.message); refresh.disabled = false; });
        });
        var del = el("button", { type: "button", class: "repo-btn", title: "Remove repo" }, "✕");
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
      '<div class="form-actions"><button type="submit" class="repo-btn primary">Add</button>' +
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

  // ---- create project -----------------------------------------------------
  function submitCreate(f, body) {
    status(f, "creating…", false);
    jfetch("/projects/create", { method: "POST", body: body })
      .then(function (res) { window.location.href = "/p/" + res.id + "/"; })
      .catch(function (err) { status(f, err.message, true); });
  }

  function openCloneForm(rp) {
    var f = el("form", { class: "sc-form" });
    f.innerHTML =
      '<p class="muted">Clone <strong>' + esc(rp.name) + '</strong> into a fresh workspace and start a project.</p>' +
      '<label>Branch (optional) <input name="branch" type="text" placeholder="' + esc(rp.default_branch || "default") + '" autocomplete="off"></label>' +
      '<div class="form-actions"><button type="submit" class="repo-btn primary">Create &amp; open</button>' +
      '<span class="form-status"></span></div>';
    f.addEventListener("submit", function (e) {
      e.preventDefault();
      submitCreate(f, { mode: "clone", repoId: rp.id, branch: f.branch.value.trim() });
    });
    openModal("New project from " + rp.name, f);
  }

  function openNewProject() {
    var wrap = el("div");
    wrap.innerHTML =
      '<div class="mode-tabs">' +
      '  <button type="button" data-mode="clone" class="active">From a repo</button>' +
      '  <button type="button" data-mode="new">New empty dir</button>' +
      '  <button type="button" data-mode="existing">Existing dir</button>' +
      '</div><div id="mode-body"></div>';
    var body = wrap.querySelector("#mode-body");
    var tabs = wrap.querySelectorAll(".mode-tabs button");
    function setMode(m) {
      tabs.forEach(function (t) { t.classList.toggle("active", t.dataset.mode === m); });
      if (m === "new") body.innerHTML =
        '<form class="sc-form"><label>Name <input name="name" type="text" placeholder="my-project" autocomplete="off"></label>' +
        '<div class="form-actions"><button type="submit" class="repo-btn primary">Create &amp; open</button><span class="form-status"></span></div></form>';
      else if (m === "existing") body.innerHTML =
        '<form class="sc-form"><label>Absolute path <input name="path" type="text" placeholder="/Users/you/code/project" autocomplete="off"></label>' +
        '<div class="form-actions"><button type="submit" class="repo-btn primary">Register &amp; open</button><span class="form-status"></span></div></form>';
      else body.innerHTML = '<p class="muted">Pick a repo from the list and click <strong>Create project</strong>, or add one first.</p>';
      var form = body.querySelector("form");
      if (form) form.addEventListener("submit", function (e) {
        e.preventDefault();
        if (m === "new") submitCreate(form, { mode: "new", name: form.name.value.trim() });
        else submitCreate(form, { mode: "existing", path: form.path.value.trim() });
      });
    }
    tabs.forEach(function (t) { t.addEventListener("click", function () { setMode(t.dataset.mode); }); });
    setMode("clone");
    openModal("New project", wrap);
  }

  document.getElementById("add-repo").addEventListener("click", openAddRepo);
  var np = document.getElementById("new-project");
  if (np) np.addEventListener("click", openNewProject);

  loadRepos();
})();
