import { useEffect, useRef } from "react";
import { Terminal } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import { wsURL } from "../api/client";

// A PTY terminal bridged to a WebSocket endpoint. Keystrokes -> binary frames ->
// PTY; PTY output -> binary frames -> xterm; xterm resize -> JSON control frame.
// Port of terminal.js, reused by the Claude dock, container shell, and host
// shell (each just passes a different wsPath). The connection opens on mount and
// closes on unmount, so a pane that isn't rendered spawns no PTY.
// Provide either (projectId + wsPath) for a project-scoped endpoint, or fullPath
// for an absolute one (e.g. the global populate-creds terminal).
export function XtermPane({ projectId, wsPath, fullPath }: { projectId?: string; wsPath?: string; fullPath?: string }) {
  const host = useRef<HTMLDivElement | null>(null);
  const path = fullPath ?? `/p/${projectId}${wsPath}`;

  useEffect(() => {
    const term = new Terminal({
      cursorBlink: true,
      fontFamily: "ui-monospace, SFMono-Regular, Menlo, Consolas, monospace",
      fontSize: 13,
      theme: { background: "#0B0E14" },
      scrollback: 10000,
    });
    const fit = new FitAddon();
    term.loadAddon(fit);
    if (host.current) {
      term.open(host.current);
      fit.fit();
    }

    const ws = new WebSocket(wsURL(path));
    ws.binaryType = "arraybuffer";
    const decoder = new TextDecoder();

    const sendResize = () => {
      if (ws.readyState === WebSocket.OPEN) ws.send(JSON.stringify({ type: "resize", cols: term.cols, rows: term.rows }));
    };

    ws.onopen = () => {
      sendResize();
      term.focus();
    };
    ws.onmessage = (ev) => {
      term.write(typeof ev.data === "string" ? ev.data : decoder.decode(new Uint8Array(ev.data)));
    };
    ws.onclose = () => {
      term.write("\r\n\x1b[90m[disconnected — reopen to reconnect]\x1b[0m\r\n");
    };
    term.onData((d) => {
      if (ws.readyState === WebSocket.OPEN) ws.send(new TextEncoder().encode(d));
    });

    let resizeTimer: number;
    const onResize = () => {
      window.clearTimeout(resizeTimer);
      resizeTimer = window.setTimeout(() => {
        fit.fit();
        sendResize();
      }, 100);
    };
    window.addEventListener("resize", onResize);
    // Fit again shortly after mount once layout settles.
    const settle = window.setTimeout(() => {
      fit.fit();
      sendResize();
    }, 50);

    return () => {
      window.removeEventListener("resize", onResize);
      window.clearTimeout(resizeTimer);
      window.clearTimeout(settle);
      try {
        ws.close();
      } catch {
        /* ignore */
      }
      term.dispose();
    };
  }, [path]);

  return <div className="term-fill" ref={host} style={{ width: "100%", height: "100%" }} />;
}
