// Reusable xterm-over-WebSocket embed. openEmbeddedTerminal(el, wsPath) attaches
// a terminal to `el` bridged to the given WS path (e.g. /global/populate/ws).
// Shares the same PTY-bridge protocol as the project terminal (binary I/O + a
// JSON resize control frame). Depends on xterm.js / xterm-addon-fit already
// loaded on the page.
function openEmbeddedTerminal(el, wsPath) {
  var term = new Terminal({
    cursorBlink: true,
    fontFamily: "ui-monospace, SFMono-Regular, Menlo, Consolas, monospace",
    fontSize: 13,
    theme: { background: "#0B0E14" },
    scrollback: 4000,
  });
  var fit = new FitAddon.FitAddon();
  term.loadAddon(fit);
  term.open(el);
  fit.fit();

  var proto = location.protocol === "https:" ? "wss:" : "ws:";
  var ws = new WebSocket(proto + "//" + location.host + wsPath);
  ws.binaryType = "arraybuffer";
  var decoder = new TextDecoder();

  function sendResize() {
    if (ws.readyState === WebSocket.OPEN) {
      ws.send(JSON.stringify({ type: "resize", cols: term.cols, rows: term.rows }));
    }
  }

  ws.onopen = function () { sendResize(); term.focus(); };
  ws.onmessage = function (ev) {
    if (typeof ev.data === "string") term.write(ev.data);
    else term.write(decoder.decode(new Uint8Array(ev.data)));
  };
  ws.onclose = function () {
    term.write("\r\n\x1b[90m[disconnected]\x1b[0m\r\n");
  };
  term.onData(function (data) {
    if (ws.readyState === WebSocket.OPEN) ws.send(new TextEncoder().encode(data));
  });

  var t;
  window.addEventListener("resize", function () {
    clearTimeout(t);
    t = setTimeout(function () { fit.fit(); sendResize(); }, 100);
  });

  return { term: term, ws: ws, dispose: function () { try { ws.close(); } catch (e) {} term.dispose(); } };
}
