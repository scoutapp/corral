// Config tab: fetch /p/<id>/config, render editable controls split into a
// live-reload zone and a restart-required zone, and drive the review→apply flow.
// Live edits go through POST /config/apply (minimal reload, no restart);
// restart-required edits go through POST /config/restart after an explicit
// confirm. Credential VALUES are only ever sent on an explicit set — the read
// side masks them.
//
// Exposed as startConfig(projectId); dashboard.js loads it on first tab open.
function startConfig(projectId) {
  var root = document.getElementById("config-root");
  if (!root) return;

  var current = null; // last-loaded server config
  var newCreds = []; // pending credential adds {host,kind,name,value}
  var removedCreds = {}; // host -> true, pending removals

  function esc(s) {
    return String(s).replace(/&/g, "&amp;").replace(/</g, "&lt;")
      .replace(/>/g, "&gt;").replace(/"/g, "&quot;");
  }

  function linesToList(id, items) {
    return '<textarea class="cfg-edit" id="' + id + '" rows="' +
      Math.max(2, (items || []).length + 1) + '" spellcheck="false">' +
      esc((items || []).join("\n")) + "</textarea>";
  }

  function textareaLines(id) {
    var el = document.getElementById(id);
    if (!el) return null;
    return el.value.split("\n").map(function (s) { return s.trim(); }).filter(Boolean);
  }

  function render(cfg) {
    current = cfg;
    newCreds = [];
    removedCreds = {};

    var statusLine = cfg.container_up
      ? '<span class="s-2xx">container running — live changes apply immediately</span>'
      : '<span class="muted">container not running — live changes save for next start</span>';

    root.innerHTML =
      '<p class="cfg-status">' + statusLine + "</p>" +

      '<section class="cfg-zone"><h3>Live <span class="muted">— hot-reloaded, no restart</span></h3>' +
        field("Allowed hosts", linesToList("cfg-allowed", cfg.allowed_hosts)) +
        field("Monitored hosts",
              '<label class="cfg-inline"><input type="checkbox" id="cfg-monitor-all" ' +
                (cfg.monitor_all ? "checked" : "") + "> monitor all allowed hosts</label>" +
              '<div id="cfg-monitor-wrap" style="' + (cfg.monitor_all ? "display:none" : "") + '">' +
                linesToList("cfg-monitor", cfg.monitor_hosts) +
                '<div class="muted cfg-note">only these hosts are decrypted; others allowed+logged but direct-dialed</div>' +
              "</div>") +
        field("Mitm ports", linesToList("cfg-ports", cfg.mitm_ports) +
              '<div class="muted cfg-note">other ports (ssh, socks, git-over-ssh) are direct-dialed</div>') +
        field("Credentials", credEditor(cfg.credentials)) +
        '<div class="cfg-actions">' +
          '<button id="cfg-review" class="cfg-btn">Review changes</button>' +
          '<span id="cfg-msg" class="cfg-msg"></span>' +
        "</div>" +
      "</section>" +

      '<section class="cfg-zone"><h3>Restart required <span class="muted">— needs a project restart</span></h3>' +
        field("Workspace", "<code>" + esc(cfg.workspace) + "</code>") +
        field("Proxy enabled", toggle("cfg-proxy", cfg.proxy_enabled)) +
        field("Docker-in-Docker", toggle("cfg-dind", cfg.dind_enabled)) +
        field("Published ports", linesToList("cfg-dindports", cfg.dind_ports)) +
        field("Launch tmux", toggle("cfg-tmux", cfg.launch_tmux)) +
        '<div class="cfg-actions">' +
          '<button id="cfg-restart" class="cfg-btn cfg-btn-danger">Restart project now</button>' +
          '<span class="muted"> — interrupts the running session in this project</span>' +
        "</div>" +
      "</section>" +

      '<div id="cfg-modal" class="cfg-modal" style="display:none"></div>';

    wire();
  }

  function field(label, valueHTML) {
    return '<div class="cfg-field"><div class="cfg-label">' + esc(label) +
      '</div><div class="cfg-value">' + valueHTML + "</div></div>";
  }
  function toggle(id, on) {
    return '<label class="cfg-inline"><input type="checkbox" id="' + id + '" ' +
      (on ? "checked" : "") + "> enabled</label>";
  }

  function credEditor(creds) {
    var rows = (creds || []).map(function (c) {
      return '<tr data-host="' + esc(c.host) + '"><td>' + esc(c.host) + "</td><td>" +
        esc(c.kind) + ": " + esc(c.name) + '</td><td class="muted">' + esc(c.masked) +
        '</td><td><button class="cfg-cred-rm" data-host="' + esc(c.host) + '">remove</button></td></tr>';
    }).join("");
    return '<table class="cfg-creds"><tbody id="cfg-cred-rows">' + rows + "</tbody></table>" +
      '<div class="cfg-cred-add">' +
        '<input id="nc-host" placeholder="host (api.foo.com)">' +
        '<select id="nc-kind"><option value="header">header</option><option value="url_param">url_param</option></select>' +
        '<input id="nc-name" placeholder="name (X-API-Key)">' +
        '<input id="nc-value" placeholder="value (secret)" type="password">' +
        '<button id="nc-add" class="cfg-btn">Add credential</button>' +
      "</div>";
  }

  // Collect the edited live-zone config into a POST payload (only changed fields).
  function collectLiveEdit() {
    var edit = {};
    var allowed = textareaLines("cfg-allowed");
    if (allowed) edit.allowed_hosts = allowed;

    var monitorAll = document.getElementById("cfg-monitor-all").checked;
    edit.monitor_hosts = monitorAll ? [] : (textareaLines("cfg-monitor") || []);

    var ports = textareaLines("cfg-ports");
    if (ports) edit.mitm_ports = ports;

    if (newCreds.length) edit.set_creds = newCreds.slice();
    var unset = Object.keys(removedCreds);
    if (unset.length) edit.unset_creds = unset;
    return edit;
  }

  function collectRestartEdit() {
    return {
      proxy_enabled: document.getElementById("cfg-proxy").checked,
      dind_enabled: document.getElementById("cfg-dind").checked,
      dind_ports: textareaLines("cfg-dindports") || [],
      launch_tmux: document.getElementById("cfg-tmux").checked,
    };
  }

  function post(path, body) {
    return fetch("/p/" + projectId + path, {
      method: "POST",
      credentials: "same-origin",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
    }).then(function (r) {
      if (!r.ok) return r.text().then(function (t) { throw new Error(t || ("HTTP " + r.status)); });
      return r.json();
    });
  }

  function showModal(html) {
    var m = document.getElementById("cfg-modal");
    m.innerHTML = html;
    m.style.display = "block";
  }
  function hideModal() { document.getElementById("cfg-modal").style.display = "none"; }

  function reviewAndApply() {
    var edit = collectLiveEdit();
    post("/config/diff", edit).then(function (res) {
      var entries = res.entries || [];
      if (!entries.length) {
        setMsg("no changes", false);
        return;
      }
      var rows = entries.map(function (e) {
        return '<tr><td class="cfg-diff-field">' + esc(e.field) + "</td><td>" + esc(e.change) + "</td></tr>";
      }).join("");
      showModal(
        '<div class="cfg-modal-box"><h3>Review changes</h3>' +
          '<table class="cfg-diff">' + rows + "</table>" +
          '<div class="cfg-modal-actions">' +
            '<button id="cfg-confirm" class="cfg-btn">Confirm & apply</button>' +
            '<button id="cfg-cancel" class="cfg-btn cfg-btn-ghost">Cancel</button>' +
          "</div></div>");
      document.getElementById("cfg-cancel").onclick = hideModal;
      document.getElementById("cfg-confirm").onclick = function () {
        this.disabled = true;
        this.textContent = "applying…";
        post("/config/apply", edit).then(function (r) {
          hideModal();
          setMsg((r.results || []).join("  •  "), false);
          reload(); // refresh to reflect applied state
        }).catch(function (err) {
          hideModal();
          setMsg("apply failed: " + err.message, true);
        });
      };
    }).catch(function (err) { setMsg("diff failed: " + err.message, true); });
  }

  function doRestart() {
    if (!window.confirm("Restart this project now? This kills the container and any running session in it.")) return;
    var btn = document.getElementById("cfg-restart");
    btn.disabled = true;
    btn.textContent = "restarting…";
    post("/config/restart", collectRestartEdit()).then(function (r) {
      setMsg((r.results || []).join("  •  "), false);
    }).catch(function (err) {
      setMsg("restart failed: " + err.message, true);
      btn.disabled = false;
      btn.textContent = "Restart project now";
    });
  }

  function setMsg(text, isErr) {
    var el = document.getElementById("cfg-msg");
    if (!el) return;
    el.textContent = text;
    el.className = "cfg-msg " + (isErr ? "s-4xx" : "s-2xx");
  }

  function wire() {
    document.getElementById("cfg-monitor-all").addEventListener("change", function () {
      document.getElementById("cfg-monitor-wrap").style.display = this.checked ? "none" : "";
    });
    document.getElementById("cfg-review").addEventListener("click", reviewAndApply);
    document.getElementById("cfg-restart").addEventListener("click", doRestart);

    document.getElementById("nc-add").addEventListener("click", function () {
      var host = document.getElementById("nc-host").value.trim().toLowerCase();
      var kind = document.getElementById("nc-kind").value;
      var name = document.getElementById("nc-name").value.trim();
      var value = document.getElementById("nc-value").value;
      if (!host || !name || !value) { setMsg("credential needs host, name, and value", true); return; }
      newCreds.push({ host: host, kind: kind, name: name, value: value });
      var tbody = document.getElementById("cfg-cred-rows");
      var tr = document.createElement("tr");
      tr.innerHTML = "<td>" + esc(host) + "</td><td>" + esc(kind) + ": " + esc(name) +
        '</td><td class="s-2xx">pending</td><td></td>';
      tbody.appendChild(tr);
      document.getElementById("nc-host").value = "";
      document.getElementById("nc-name").value = "";
      document.getElementById("nc-value").value = "";
      setMsg("credential queued — click Review changes to apply", false);
    });

    Array.prototype.forEach.call(document.querySelectorAll(".cfg-cred-rm"), function (b) {
      b.addEventListener("click", function () {
        var host = this.dataset.host;
        removedCreds[host] = true;
        var row = this.closest("tr");
        if (row) row.style.opacity = "0.4";
        setMsg("credential " + host + " queued for removal — Review to apply", false);
      });
    });
  }

  function reload() {
    fetch("/p/" + projectId + "/config", { credentials: "same-origin" })
      .then(function (r) { if (!r.ok) throw new Error("HTTP " + r.status); return r.json(); })
      .then(render)
      .catch(function (err) {
        root.innerHTML = '<p class="s-4xx">failed to load config: ' + esc(err.message) + "</p>";
      });
  }

  reload();
}
