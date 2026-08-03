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
        '<h3>Load SSH keys <span class="sshload-hostbadge">host shell</span></h3>' +
        '<p class="muted">This is a real shell <strong>on your host machine</strong> ' +
        "(not sandboxed). It runs <code>ssh-add</code> for this project's scoped " +
        "agent — type each key's passphrase at the prompt. The passphrase goes " +
        "straight to <code>ssh-add</code> in the terminal; it's never stored or " +
        "sent as data.</p>" +
        '<div class="sshload-term"></div>' +
        '<div class="sshload-actions">' +
          '<button class="cfg-btn cfg-btn-ghost sshload-done">Cancel</button>' +
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
    // The PTY now runs a persistent interactive host shell (ssh-add, then a live
    // prompt), so the WS does NOT close when ssh-add finishes. Instead POLL the
    // load status: once the scoped agent holds the keys, reveal a "Continue"
    // button (and highlight it) so the user confirms the close on their terms.
    var poll = setInterval(function () {
      fetch("/p/" + encodeURIComponent(projectId) + "/sshkeys/status", { credentials: "same-origin" })
        .then(function (r) { return r.ok ? r.json() : null; })
        .then(function (s) { if (s && s.loaded) onLoaded(); })
        .catch(function () {});
    }, 1500);
    ws.onclose = function () {
      // Shell exited (user typed `exit`, or it died). Report whatever state we're
      // in based on the last poll.
      finish(loadedSeen);
    };
    term.onData(function (d) { if (ws.readyState === WebSocket.OPEN) ws.send(new TextEncoder().encode(d)); });

    var done = false;
    var loadedSeen = false;
    function finish(loaded) {
      if (done) return;
      done = true;
      clearInterval(poll);
      try { ws.close(); } catch (e) {}
      try { term.dispose(); } catch (e) {}
      overlay.remove();
      if (typeof onDone === "function") onDone(loaded);
    }

    // Keys detected as loaded: swap the actions for a highlighted Continue (which
    // proceeds — e.g. auto-starts the project). Fires once.
    function onLoaded() {
      if (loadedSeen) return;
      loadedSeen = true;
      term.write("\r\n\x1b[32m✓ keys loaded — click Continue to proceed\x1b[0m\r\n");
      var actions = overlay.querySelector(".sshload-actions");
      if (actions) {
        actions.innerHTML = '<button class="cfg-btn sshload-continue">Continue ▶</button>';
        actions.querySelector(".sshload-continue").onclick = function () { finish(true); };
      }
    }

    // Cancel closes without loading (the shell is still live; user changed mind
    // or the passphrase was wrong — they can just click ▶ Start again later).
    overlay.querySelector(".sshload-done").onclick = function () { finish(false); };
    // Click outside the box cancels too.
    overlay.addEventListener("click", function (e) { if (e.target === overlay) finish(false); });
  }

  window.openSSHLoadModal = openSSHLoadModal;
})();
