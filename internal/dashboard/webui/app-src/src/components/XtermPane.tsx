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
    const sendBytes = (s: string) => {
      if (ws.readyState === WebSocket.OPEN) ws.send(new TextEncoder().encode(s));
    };
    term.onData((d) => sendBytes(d));

    // Enter handling, made explicit so it doesn't depend on xterm's keyboard-mode
    // negotiation (which can leave a bare Enter producing a CSI-u sequence the app
    // ignores — the "Enter does nothing" symptom):
    //   • plain Enter          → CR (\r): submit
    //   • Cmd/Ctrl/Alt+Enter   → ESC+CR (\x1b\r): the meta-return newline apps read
    //     as "insert a newline, don't submit"
    // Returning false stops xterm's own default so we don't double-send.
    term.attachCustomKeyEventHandler((e) => {
      if (e.type !== "keydown" || e.key !== "Enter") return true;
      // preventDefault + returning false stops xterm from ALSO emitting its own
      // \r for this key (which double-submitted).
      e.preventDefault();
      sendBytes(e.metaKey || e.ctrlKey || e.altKey ? "\x1b\r" : "\r");
      return false;
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

    // Refit when the PANE's own box changes size — not just the window. This is
    // what makes dragging the dock/overlay/chat resize handles actually reflow the
    // terminal (cols/rows) instead of just stretching the container around a
    // fixed-size grid. Debounced through the same timer as window resizes.
    const ro = new ResizeObserver(() => onResize());
    if (host.current) ro.observe(host.current);

    // Fit again shortly after mount once layout settles.
    const settle = window.setTimeout(() => {
      fit.fit();
      sendResize();
    }, 50);

    return () => {
      window.removeEventListener("resize", onResize);
      ro.disconnect();
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
