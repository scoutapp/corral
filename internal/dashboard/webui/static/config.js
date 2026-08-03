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
        field("Capture (mitm)",
              '<select id="cfg-mitm-preset" class="cfg-select">' +
                presetOption("minimal", "Minimal — Claude + GitHub only", cfg.mitm_preset) +
                presetOption("all", "All — every allowed host", cfg.mitm_preset) +
                presetOption("none", "None — decrypt nothing", cfg.mitm_preset) +
                presetOption("custom", "Custom — the host list below", cfg.mitm_preset) +
              "</select>" +
              '<div id="cfg-monitor-wrap" style="' + (cfg.mitm_preset === "custom" ? "" : "display:none") + '">' +
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
        field("Seccomp",
              '<select id="cfg-seccomp" class="cfg-select">' +
                seccompOption("", "Default (Docker profile)", cfg.seccomp_mode) +
                seccompOption("unconfined", "Unconfined — no filtering", cfg.seccomp_mode) +
              "</select>" +
              '<div class="muted cfg-note">unconfined allows syscalls the default profile blocks (e.g. Erlang/BEAM). No effect with Docker-in-Docker (already privileged).</div>') +
      field("SSH keys", sshKeysEditor(cfg)) +
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
  function presetOption(val, label, current) {
    return '<option value="' + val + '"' + (current === val ? " selected" : "") + ">" + esc(label) + "</option>";
  }
  function seccompOption(val, label, current) {
    // treat "default" and "" as the same selected state
    var cur = current === "default" ? "" : (current || "");
    return '<option value="' + val + '"' + (cur === val ? " selected" : "") + ">" + esc(label) + "</option>";
  }

  // SSH scoped-agent editor (union model: global default ∪ project extras).
  // A checklist seeded from ~/.ssh — check the keys to add for THIS project. Keys
  // already in the global default show pre-checked + locked ("from global"),
  // managed in global settings. Renders a placeholder; fillSSHKeyPicker() fetches
  // ~/.ssh and paints the list async. A "+ other path" adds keys outside ~/.ssh.
  function sshKeysEditor(cfg) {
    return (
      '<div id="cfg-ssh-picker"><span class="muted">loading keys…</span></div>' +
      '<div class="cfg-ssh-extra-add">' +
        '<input id="cfg-ssh-otherpath" placeholder="other key path (~/.ssh/... or /abs/path)">' +
        '<button id="cfg-ssh-addpath" class="cfg-btn cfg-btn-ghost" type="button">+ add path</button>' +
      "</div>" +
      '<div id="cfg-ssh-eff" class="muted cfg-note"></div>' +
      '<div class="cfg-ssh-load">' +
        '<span id="cfg-ssh-status" class="muted">checking…</span> ' +
        '<button id="cfg-ssh-load-btn" class="cfg-btn" type="button">Load keys…</button>' +
      "</div>" +
      '<div id="cfg-ssh-modal" class="cfg-modal" style="display:none"></div>'
    );
  }

  // Normalize a key reference for comparison: basename without a dir, so
  // "~/.ssh/github", "github", and "/Users/x/.ssh/github" match. Global keys and
  // project extras are compared on this so we don't double-list.
  function sshKeyBasename(p) {
    return String(p).replace(/^.*\//, "");
  }

  // Fetches ~/.ssh keys + the current global/project sets, then paints the
  // checklist. Called after render(). `current` holds the last-loaded config.
  function fillSSHKeyPicker() {
    var host = document.getElementById("cfg-ssh-picker");
    if (!host || !current) return;
    var globalSet = {};
    (current.ssh_keys_global || []).forEach(function (k) { globalSet[sshKeyBasename(k)] = true; });
    var projSet = {};
    (current.ssh_keys || []).forEach(function (k) { projSet[sshKeyBasename(k)] = true; });

    fetch("/p/" + projectId + "/sshkeys/available", { credentials: "same-origin" })
      .then(function (r) { return r.ok ? r.json() : { keys: [] }; })
      .then(function (res) {
        var avail = res.keys || [];
        var seen = {};
        var rows = avail.map(function (k) {
          seen[k.name] = true;
          var inGlobal = globalSet[k.name];
          var inProj = projSet[k.name];
          var meta = [k.type, k.comment].filter(Boolean).join("  ");
          return sshKeyRow(k.name, inGlobal, inGlobal || inProj, meta);
        });
        // Project extras that point outside ~/.ssh (no matching available key):
        // still show them as checked rows so they can be seen/removed.
        (current.ssh_keys || []).forEach(function (p) {
          var b = sshKeyBasename(p);
          if (!seen[b]) rows.push(sshKeyRow(p, false, true, "custom path"));
        });
        host.innerHTML = rows.length
          ? '<div class="cfg-ssh-list">' + rows.join("") + "</div>"
          : '<div class="muted cfg-note">no keys found under ~/.ssh</div>';
        updateSSHEffective();
        wireSSHPicker();
      })
      .catch(function () { host.innerHTML = '<div class="s-4xx">failed to list ~/.ssh keys</div>'; });
  }

  // One checklist row. locked = it's a global key (checked, disabled, labeled).
  function sshKeyRow(value, locked, checked, meta) {
    return (
      '<label class="cfg-ssh-item' + (locked ? " locked" : "") + '">' +
        '<input type="checkbox" class="cfg-ssh-key" value="' + esc(value) + '"' +
          (checked ? " checked" : "") + (locked ? " disabled" : "") + "> " +
        '<span class="cfg-ssh-name">' + esc(sshKeyBasename(value)) + "</span> " +
        (meta ? '<span class="muted cfg-ssh-meta">' + esc(meta) + "</span>" : "") +
        (locked ? '<span class="cfg-ssh-badge">from global</span>' : "") +
      "</label>"
    );
  }

  // Collect the project EXTRAS = checked, non-locked keys (locked = global,
  // managed elsewhere, never written to the project list).
  function collectSSHExtras() {
    var out = [];
    Array.prototype.forEach.call(document.querySelectorAll(".cfg-ssh-key"), function (cb) {
      if (cb.checked && !cb.disabled) out.push(cb.value);
    });
    return out;
  }

  // Effective preview = global (locked) + checked extras, deduped by basename.
  function updateSSHEffective() {
    var el = document.getElementById("cfg-ssh-eff");
    if (!el) return;
    var names = {};
    (current.ssh_keys_global || []).forEach(function (k) { names[sshKeyBasename(k)] = true; });
    collectSSHExtras().forEach(function (k) { names[sshKeyBasename(k)] = true; });
    var list = Object.keys(names);
    el.textContent = list.length ? "will load: " + list.join(", ") : "no keys — no ssh-agent is mounted";
  }

  // Re-run the effective preview whenever a checkbox toggles.
  function wireSSHPicker() {
    Array.prototype.forEach.call(document.querySelectorAll(".cfg-ssh-key"), function (cb) {
      cb.addEventListener("change", updateSSHEffective);
    });
  }

  // Add a key by explicit path (outside ~/.ssh). Appends a checked, editable row
  // to the list and clears the input.
  function addSSHOtherPath() {
    var input = document.getElementById("cfg-ssh-otherpath");
    var val = input.value.trim();
    if (!val) return;
    var list = document.querySelector(".cfg-ssh-list");
    if (!list) {
      var host = document.getElementById("cfg-ssh-picker");
      host.innerHTML = '<div class="cfg-ssh-list"></div>';
      list = host.querySelector(".cfg-ssh-list");
    }
    var tmp = document.createElement("div");
    tmp.innerHTML = sshKeyRow(val, false, true, "custom path");
    list.appendChild(tmp.firstChild);
    input.value = "";
    wireSSHPicker();
    updateSSHEffective();
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

    var preset = document.getElementById("cfg-mitm-preset").value;
    edit.mitm_preset = preset;
    // In custom mode the host list is authoritative; the backend applies it.
    if (preset === "custom") edit.monitor_hosts = textareaLines("cfg-monitor") || [];

    var ports = textareaLines("cfg-ports");
    if (ports) edit.mitm_ports = ports;

    if (newCreds.length) edit.set_creds = newCreds.slice();
    var unset = Object.keys(removedCreds);
    if (unset.length) edit.unset_creds = unset;
    return edit;
  }

  function collectRestartEdit() {
    var edit = {
      proxy_enabled: document.getElementById("cfg-proxy").checked,
      dind_enabled: document.getElementById("cfg-dind").checked,
      dind_ports: textareaLines("cfg-dindports") || [],
      launch_tmux: document.getElementById("cfg-tmux").checked,
      seccomp_mode: document.getElementById("cfg-seccomp").value,
    };
    // SSH project extras = checked, non-locked keys (global keys are locked and
    // managed in global settings, never written to the project list).
    edit.ssh_keys = collectSSHExtras();
    return edit;
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
    document.getElementById("cfg-mitm-preset").addEventListener("change", function () {
      document.getElementById("cfg-monitor-wrap").style.display = this.value === "custom" ? "" : "none";
    });
    document.getElementById("cfg-review").addEventListener("click", reviewAndApply);
    document.getElementById("cfg-restart").addEventListener("click", doRestart);

    var sshLoad = document.getElementById("cfg-ssh-load-btn");
    if (sshLoad) sshLoad.addEventListener("click", openSSHLoadModal);
    var addPath = document.getElementById("cfg-ssh-addpath");
    if (addPath) addPath.addEventListener("click", addSSHOtherPath);
    fillSSHKeyPicker(); // paints the checklist async, then wires it + effective preview
    refreshSSHStatus();

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

  // Poll the scoped-agent status so the user sees whether keys are already loaded
  // (and won't be re-prompted on start) vs. need loading.
  function refreshSSHStatus() {
    var el = document.getElementById("cfg-ssh-status");
    if (!el) return;
    fetch("/p/" + projectId + "/sshkeys/status", { credentials: "same-origin" })
      .then(function (r) { return r.ok ? r.json() : null; })
      .then(function (s) {
        if (!s) { el.textContent = ""; return; }
        if (!s.configured) { el.textContent = "no keys configured"; el.className = "muted"; return; }
        if (s.loaded) {
          el.textContent = "✓ " + s.count + " key(s) loaded — start won't prompt";
          el.className = "s-2xx";
        } else {
          el.textContent = "not loaded — start will need passphrases";
          el.className = "muted";
        }
      })
      .catch(function () { if (el) el.textContent = ""; });
  }

  // Load modal: an inline xterm bridged to /sshkeys/ws, which runs `ssh-add` for
  // the scoped agent so passphrase prompts appear in a real PTY. When ssh-add
  // exits the WS closes; we re-check status. Keys never leave the PTY.
  function openSSHLoadModal() {
    var m = document.getElementById("cfg-ssh-modal");
    if (!m || typeof Terminal === "undefined") {
      setMsg("terminal unavailable", true);
      return;
    }
    m.innerHTML =
      '<div class="cfg-modal-box"><h3>Load SSH keys</h3>' +
        '<p class="muted">Type each key\'s passphrase below. Keys load into this project\'s scoped agent; they are never stored.</p>' +
        '<div id="cfg-ssh-term" style="height:220px;background:#0B0E14;border-radius:6px;overflow:hidden"></div>' +
        '<div class="cfg-modal-actions">' +
          '<button id="cfg-ssh-done" class="cfg-btn">Done</button>' +
        "</div></div>";
    m.style.display = "block";

    var term = new Terminal({
      cursorBlink: true,
      fontFamily: "ui-monospace, SFMono-Regular, Menlo, Consolas, monospace",
      fontSize: 13, theme: { background: "#0B0E14" }, scrollback: 1000,
    });
    var fit = new FitAddon.FitAddon();
    term.loadAddon(fit);
    term.open(document.getElementById("cfg-ssh-term"));
    fit.fit();

    var proto = location.protocol === "https:" ? "wss:" : "ws:";
    var ws = new WebSocket(proto + "//" + location.host + "/p/" + projectId + "/sshkeys/ws");
    ws.binaryType = "arraybuffer";
    var decoder = new TextDecoder();
    ws.onopen = function () {
      ws.send(JSON.stringify({ type: "resize", cols: term.cols, rows: term.rows }));
      term.focus();
    };
    ws.onmessage = function (ev) {
      term.write(typeof ev.data === "string" ? ev.data : decoder.decode(new Uint8Array(ev.data)));
    };
    ws.onclose = function () {
      term.write("\r\n\x1b[90m[ssh-add finished — click Done]\x1b[0m\r\n");
      refreshSSHStatus();
    };
    term.onData(function (d) { if (ws.readyState === WebSocket.OPEN) ws.send(new TextEncoder().encode(d)); });

    function close() {
      try { ws.close(); } catch (e) {}
      try { term.dispose(); } catch (e) {}
      m.style.display = "none";
      refreshSSHStatus();
    }
    document.getElementById("cfg-ssh-done").onclick = close;
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
