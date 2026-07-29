// Browser-side of the PTY terminal. Pairs with handleTerminalWS in terminal.go:
// keystrokes → binary WS frames → PTY; PTY output → binary WS frames → xterm;
// xterm resize → JSON control frame → pty.Setsize. Ships with the dashboard
// (no external terminal program), which is why it replaced ttyd.
(function () {
  var id = document.body.dataset.id;
  if (!id || document.body.dataset.sessionUp !== "true") return;
  // Which PTY endpoint to bridge to. Defaults to the tmux dev terminal; the
  // container shell page sets data-ws-path="/container/ws" to reuse this client.
  var wsPath = document.body.dataset.wsPath || "/terminal/ws";

  var term = new Terminal({
    cursorBlink: true,
    fontFamily: "ui-monospace, SFMono-Regular, Menlo, Consolas, monospace",
    fontSize: 13,
    theme: { background: "#0B0E14" },
    scrollback: 10000,
  });
  var fit = new FitAddon.FitAddon();
  term.loadAddon(fit);
  term.open(document.getElementById("term"));
  fit.fit();

  // ws(s):// mirrors the page's http(s) scheme so the terminal works whether the
  // dashboard is ever fronted by TLS or (as today) plain loopback HTTP.
  var proto = location.protocol === "https:" ? "wss:" : "ws:";
  var ws = new WebSocket(proto + "//" + location.host + "/p/" + id + wsPath);
  ws.binaryType = "arraybuffer";

  var decoder = new TextDecoder();

  function sendResize() {
    if (ws.readyState !== WebSocket.OPEN) return;
    ws.send(JSON.stringify({ type: "resize", cols: term.cols, rows: term.rows }));
  }

  ws.onopen = function () {
    sendResize(); // sync the PTY to xterm's actual size before first draw
    term.focus();
  };

  ws.onmessage = function (ev) {
    // Server sends terminal output as binary; the only text frames are close
    // reasons / error notices, which are still fine to write verbatim.
    if (typeof ev.data === "string") {
      term.write(ev.data);
    } else {
      term.write(decoder.decode(new Uint8Array(ev.data)));
    }
  };

  ws.onclose = function () {
    term.write("\r\n\x1b[90m[disconnected — reload to reconnect]\x1b[0m\r\n");
  };

  // Keystrokes → PTY as raw bytes (UTF-8), never coalesced with control frames.
  term.onData(function (data) {
    if (ws.readyState === WebSocket.OPEN) {
      ws.send(new TextEncoder().encode(data));
    }
  });

  var resizeTimer;
  window.addEventListener("resize", function () {
    clearTimeout(resizeTimer);
    resizeTimer = setTimeout(function () {
      fit.fit();
      sendResize();
    }, 100);
  });
})();
