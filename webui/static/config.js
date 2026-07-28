// Config tab (read side, Layer 4): fetch /p/<id>/config and render the project's
// settings, split into a live-reload zone and a restart-required zone. Editing +
// Apply is layered on in config-apply.js (Layer 5); this renders current state.
//
// Exposed as a global (startConfig) so dashboard.js can load it on first tab open.
function startConfig(projectId) {
  var root = document.getElementById("config-root");
  if (!root) return;

  function esc(s) {
    return String(s).replace(/&/g, "&amp;").replace(/</g, "&lt;")
      .replace(/>/g, "&gt;").replace(/"/g, "&quot;");
  }

  function list(items, empty) {
    if (!items || !items.length) return '<span class="muted">' + esc(empty) + "</span>";
    return '<ul class="cfg-list">' +
      items.map(function (x) { return "<li>" + esc(x) + "</li>"; }).join("") + "</ul>";
  }

  function credRows(creds) {
    if (!creds || !creds.length) return '<span class="muted">none</span>';
    return '<table class="cfg-creds"><thead><tr><th>Host</th><th>Injects</th><th>Value</th></tr></thead><tbody>' +
      creds.map(function (c) {
        return "<tr><td>" + esc(c.host) + "</td><td>" + esc(c.kind) + ": " + esc(c.name) +
          '</td><td class="muted">' + esc(c.masked) + "</td></tr>";
      }).join("") + "</tbody></table>";
  }

  function render(cfg) {
    var monitorLabel = cfg.monitor_all
      ? '<span class="cfg-badge">monitor all allowed hosts</span>'
      : '<span class="cfg-badge cfg-badge-sel">only listed hosts</span>';

    var statusLine = cfg.container_up
      ? '<span class="s-2xx">container running — live changes apply immediately</span>'
      : '<span class="muted">container not running — changes save for next start</span>';

    root.innerHTML =
      '<p class="cfg-status">' + statusLine + "</p>" +

      '<section class="cfg-zone"><h3>Live <span class="muted">— hot-reloaded, no restart</span></h3>' +
        field("Allowed hosts", list(cfg.allowed_hosts, "none")) +
        field("Monitored hosts " + monitorLabel,
              cfg.monitor_all ? '<span class="muted">(all)</span>' : list(cfg.monitor_hosts, "none")) +
        field("Mitm ports", list(cfg.mitm_ports, "80,443") +
              '<div class="muted cfg-note">other ports (ssh, socks, git-over-ssh) are direct-dialed</div>') +
        field("Credentials", credRows(cfg.credentials)) +
      "</section>" +

      '<section class="cfg-zone"><h3>Restart required <span class="muted">— needs a project restart</span></h3>' +
        field("Workspace", '<code>' + esc(cfg.workspace) + "</code>") +
        field("Proxy enabled", bool(cfg.proxy_enabled)) +
        field("Docker-in-Docker", bool(cfg.dind_enabled)) +
        field("Published ports", list(cfg.dind_ports, "none")) +
        field("Launch tmux", bool(cfg.launch_tmux)) +
      "</section>";
  }

  function field(label, valueHTML) {
    return '<div class="cfg-field"><div class="cfg-label">' + esc(label) +
      '</div><div class="cfg-value">' + valueHTML + "</div></div>";
  }

  function bool(b) {
    return b ? '<span class="s-2xx">on</span>' : '<span class="muted">off</span>';
  }

  fetch("/p/" + projectId + "/config", { credentials: "same-origin" })
    .then(function (r) {
      if (!r.ok) throw new Error("HTTP " + r.status);
      return r.json();
    })
    .then(render)
    .catch(function (err) {
      root.innerHTML = '<p class="s-4xx">failed to load config: ' + esc(err.message) + "</p>";
    });
}
