// Global settings page: shared credentials (partial-masked), cross-project
// defaults (monitor-list, mitm-ports) new projects inherit, and a Populate
// button that runs the interactive `claude setup-token` flow in a bridged
// terminal right in the page.
(function () {
  var root = document.getElementById("global-root");
  if (!root) return;

  var newCreds = [];
  var removedCreds = {};
  var populateTerm = null;

  function esc(s) {
    return String(s == null ? "" : s).replace(/&/g, "&amp;").replace(/</g, "&lt;")
      .replace(/>/g, "&gt;").replace(/"/g, "&quot;");
  }

  function textareaLines(id) {
    var el = document.getElementById(id);
    if (!el) return null;
    return el.value.split("\n").map(function (s) { return s.trim(); }).filter(Boolean);
  }

  function credRows(creds) {
    if (!creds || !creds.length) return '<tr><td class="muted" colspan="4">none set</td></tr>';
    return creds.map(function (c) {
      return '<tr><td>' + esc(c.host) + "</td><td>" + esc(c.kind) + ": " + esc(c.name) +
        '</td><td class="cred-mask">' + esc(c.masked) + '</td>' +
        '<td><button class="cfg-cred-rm" data-host="' + esc(c.host) + '">remove</button></td></tr>';
    }).join("");
  }

  function render(g) {
    newCreds = [];
    removedCreds = {};
    root.innerHTML =
      '<section class="cfg-zone"><h3>Shared credentials <span class="muted">— all projects, mtime-reloaded live</span></h3>' +
        '<div class="muted global-path">' + esc(g.creds_path) + "</div>" +
        '<table class="cfg-creds"><thead><tr><th>Host</th><th>Injects</th><th>Value</th><th></th></tr></thead>' +
          '<tbody id="g-cred-rows">' + credRows(g.credentials) + "</tbody></table>" +
        '<div class="cfg-cred-add">' +
          '<input id="gnc-host" placeholder="host (api.foo.com)">' +
          '<select id="gnc-kind"><option value="header">header</option><option value="url_param">url_param</option></select>' +
          '<input id="gnc-name" placeholder="name (X-API-Key)">' +
          '<input id="gnc-value" placeholder="value (secret)" type="password">' +
          '<button id="gnc-add" class="cfg-btn">Add</button>' +
        "</div>" +
        '<div class="cfg-actions">' +
          '<button id="g-populate" class="cfg-btn cfg-btn-ghost">Populate from Claude…</button>' +
          '<span class="muted">runs <code>claude setup-token</code> in a terminal below</span>' +
        "</div>" +
        '<div id="g-populate-term" class="populate-term" style="display:none"></div>' +
      "</section>" +

      '<section class="cfg-zone"><h3>Defaults for new projects <span class="muted">— inherited at <code>sandclaude init</code></span></h3>' +
        field("Default monitor-list",
          '<textarea class="cfg-edit" id="g-monitor" rows="3" spellcheck="false">' + esc((g.monitor_hosts || []).join("\n")) + "</textarea>" +
          '<div class="muted cfg-note">empty = new projects monitor all allowed hosts</div>') +
        field("Default mitm-ports",
          '<textarea class="cfg-edit" id="g-ports" rows="2" spellcheck="false">' + esc((g.mitm_ports || []).join("\n")) + "</textarea>") +
      "</section>" +

      '<div class="cfg-actions">' +
        '<button id="g-apply" class="cfg-btn">Apply</button>' +
        '<span id="g-msg" class="cfg-msg"></span>' +
      "</div>";

    wire();
  }

  function field(label, valueHTML) {
    return '<div class="cfg-field"><div class="cfg-label">' + esc(label) +
      '</div><div class="cfg-value">' + valueHTML + "</div></div>";
  }

  function setMsg(text, isErr) {
    var el = document.getElementById("g-msg");
    if (el) { el.textContent = text; el.className = "cfg-msg " + (isErr ? "s-4xx" : "s-2xx"); }
  }

  function post(path, body) {
    return fetch(path, {
      method: "POST", credentials: "same-origin",
      headers: { "Content-Type": "application/json" }, body: JSON.stringify(body || {}),
    }).then(function (r) {
      if (!r.ok) return r.text().then(function (t) { throw new Error(t || ("HTTP " + r.status)); });
      return r.json();
    });
  }

  function apply() {
    var edit = {};
    if (newCreds.length) edit.set_creds = newCreds.slice();
    var unset = Object.keys(removedCreds);
    if (unset.length) edit.unset_creds = unset;
    edit.monitor_hosts = textareaLines("g-monitor") || [];
    edit.mitm_ports = textareaLines("g-ports") || [];

    document.getElementById("g-apply").disabled = true;
    post("/global/apply", edit).then(function (r) {
      setMsg((r.results || []).join("  •  "), false);
      reload();
    }).catch(function (err) {
      setMsg("apply failed: " + err.message, true);
      document.getElementById("g-apply").disabled = false;
    });
  }

  function populate() {
    var wrap = document.getElementById("g-populate-term");
    wrap.style.display = "block";
    wrap.innerHTML = '<div class="muted">starting…</div>';
    post("/global/populate", {}).then(function () {
      wrap.innerHTML = '<div class="screen-bar"><i class="screen-dot"></i>claude setup-token · answer the prompts</div><div class="screen-body-host"></div>';
      if (populateTerm) populateTerm.dispose();
      populateTerm = openEmbeddedTerminal(wrap.querySelector(".screen-body-host"), "/global/populate/ws");
      setMsg("complete the prompts in the terminal; credentials refresh here when done", false);
    }).catch(function (err) {
      wrap.innerHTML = '<div class="s-4xx">could not start: ' + esc(err.message) + "</div>";
    });
  }

  function wire() {
    document.getElementById("g-apply").addEventListener("click", apply);
    document.getElementById("g-populate").addEventListener("click", populate);

    document.getElementById("gnc-add").addEventListener("click", function () {
      var host = document.getElementById("gnc-host").value.trim().toLowerCase();
      var kind = document.getElementById("gnc-kind").value;
      var name = document.getElementById("gnc-name").value.trim();
      var value = document.getElementById("gnc-value").value;
      if (!host || !name || !value) { setMsg("credential needs host, name, and value", true); return; }
      newCreds.push({ host: host, kind: kind, name: name, value: value });
      var tbody = document.getElementById("g-cred-rows");
      var tr = document.createElement("tr");
      tr.innerHTML = "<td>" + esc(host) + "</td><td>" + esc(kind) + ": " + esc(name) +
        '</td><td class="s-2xx">pending</td><td></td>';
      tbody.appendChild(tr);
      ["gnc-host", "gnc-name", "gnc-value"].forEach(function (id) { document.getElementById(id).value = ""; });
      setMsg("credential queued — click Apply", false);
    });

    Array.prototype.forEach.call(document.querySelectorAll(".cfg-cred-rm"), function (b) {
      b.addEventListener("click", function () {
        removedCreds[this.dataset.host] = true;
        var row = this.closest("tr");
        if (row) row.style.opacity = "0.4";
        setMsg("credential queued for removal — click Apply", false);
      });
    });
  }

  function reload() {
    fetch("/global/config", { credentials: "same-origin" })
      .then(function (r) { if (!r.ok) throw new Error("HTTP " + r.status); return r.json(); })
      .then(render)
      .catch(function (err) { root.innerHTML = '<p class="s-4xx">failed to load: ' + esc(err.message) + "</p>"; });
  }

  reload();
})();
