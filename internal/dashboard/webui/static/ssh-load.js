// Shared SSH-key load modal. Exposes a global openSSHLoadModal(projectId, onDone).
//
// Opens a centered overlay containing an xterm bridged to
// /p/<projectId>/sshkeys/ws, which runs `ssh-add` for that project's scoped
// agent — so a passphrase prompt appears in a real PTY. Keys never leave the PTY;
// the passphrase is typed into the terminal and read by ssh-add directly.
//
// Used by BOTH the project Config tab (config.js) and the landing-page Start
// button (panes.js), so a keyed project can be started/loaded without navigating
// away. Requires xterm.js + xterm-addon-fit to be loaded on the page.
//
// onDone(loaded) is called when the modal closes: `loaded` is best-effort true if
// the agent reported identities after the load (checked via /sshkeys/status).
(function () {
  function openSSHLoadModal(projectId, onDone) {
    if (typeof Terminal === "undefined" || typeof FitAddon === "undefined") {
      // No terminal available on this page — fail loudly rather than silently.
      if (typeof onDone === "function") onDone(false);
      return;
    }

    // Build a self-contained overlay (independent of any page-specific element).
    var overlay = document.createElement("div");
    overlay.className = "sshload-overlay";
    overlay.innerHTML =
      '<div class="sshload-box">' +
        "<h3>Load SSH keys</h3>" +
        '<p class="muted">Type each key\'s passphrase below. Keys load into this ' +
        "project's scoped agent; they are never stored.</p>" +
        '<div class="sshload-term"></div>' +
        '<div class="sshload-actions">' +
          '<button class="cfg-btn sshload-done">Done</button>' +
        "</div>" +
      "</div>";
    document.body.appendChild(overlay);

    var term = new Terminal({
      cursorBlink: true,
      fontFamily: "ui-monospace, SFMono-Regular, Menlo, Consolas, monospace",
      fontSize: 13, theme: { background: "#0B0E14" }, scrollback: 1000,
    });
    var fit = new FitAddon.FitAddon();
    term.loadAddon(fit);
    term.open(overlay.querySelector(".sshload-term"));
    fit.fit();

    var proto = location.protocol === "https:" ? "wss:" : "ws:";
    var ws = new WebSocket(proto + "//" + location.host + "/p/" + encodeURIComponent(projectId) + "/sshkeys/ws");
    ws.binaryType = "arraybuffer";
    var decoder = new TextDecoder();
    ws.onopen = function () {
      ws.send(JSON.stringify({ type: "resize", cols: term.cols, rows: term.rows }));
      term.focus();
    };
    ws.onmessage = function (ev) {
      term.write(typeof ev.data === "string" ? ev.data : decoder.decode(new Uint8Array(ev.data)));
    };
    // When ssh-add exits the WS closes. Check whether the keys actually loaded:
    //   loaded -> auto-close the modal and report success (caller auto-starts).
    //   not    -> keep it open (e.g. wrong passphrase), offer Retry / Cancel.
    ws.onclose = function () {
      fetch("/p/" + encodeURIComponent(projectId) + "/sshkeys/status", { credentials: "same-origin" })
        .then(function (r) { return r.ok ? r.json() : null; })
        .then(function (s) {
          if (s && s.loaded) {
            term.write("\r\n\x1b[32m[keys loaded — starting]\x1b[0m\r\n");
            finish(true); // auto-close on success
          } else {
            term.write("\r\n\x1b[33m[keys not loaded — check the passphrase and retry]\x1b[0m\r\n");
            showRetry();
          }
        })
        .catch(function () { showRetry(); });
    };
    term.onData(function (d) { if (ws.readyState === WebSocket.OPEN) ws.send(new TextEncoder().encode(d)); });

    var done = false;
    function finish(loaded) {
      if (done) return;
      done = true;
      try { ws.close(); } catch (e) {}
      try { term.dispose(); } catch (e) {}
      overlay.remove();
      if (typeof onDone === "function") onDone(loaded);
    }

    // On failure, swap the (now stale) Done button for Retry + Cancel.
    function showRetry() {
      var actions = overlay.querySelector(".sshload-actions");
      if (!actions) return;
      actions.innerHTML =
        '<button class="cfg-btn sshload-retry">Retry</button>' +
        '<button class="cfg-btn cfg-btn-ghost sshload-cancel">Cancel</button>';
      actions.querySelector(".sshload-retry").onclick = function () {
        finish(false);                       // tear down this modal…
        openSSHLoadModal(projectId, onDone); // …and reopen a fresh one to try again
      };
      actions.querySelector(".sshload-cancel").onclick = function () { finish(false); };
    }

    // Before ssh-add finishes, the only button is Done — it cancels the load.
    overlay.querySelector(".sshload-done").onclick = function () { finish(false); };
    // Click outside the box cancels too.
    overlay.addEventListener("click", function (e) { if (e.target === overlay) finish(false); });
  }

  window.openSSHLoadModal = openSSHLoadModal;
})();
