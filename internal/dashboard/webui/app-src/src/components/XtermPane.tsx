import { useEffect, useRef, useState } from "react";
import { Terminal } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import { wsURL, postJSON } from "../api/client";
import { TerminalMenu, type TermAction } from "./TerminalMenu";

// A PTY terminal bridged to a WebSocket endpoint. Keystrokes -> binary frames ->
// PTY; PTY output -> binary frames -> xterm; xterm resize -> JSON control frame.
// Port of terminal.js, reused by the Claude dock, container shell, and host
// shell (each just passes a different wsPath). The connection opens on mount and
// closes on unmount, so a pane that isn't rendered spawns no PTY.
// Provide either (projectId + wsPath) for a project-scoped endpoint, or fullPath
// for an absolute one (e.g. the global populate-creds terminal).
// `kind` enables the right-click menu's tmux actions (split/close) for the
// tmux-backed terminals (claude/host); container/absent = copy/paste/clear only.
export function XtermPane({ projectId, wsPath, fullPath, kind }: { projectId?: string; wsPath?: string; fullPath?: string; kind?: "claude" | "host" | "container" }) {
  const host = useRef<HTMLDivElement | null>(null);
  const termRef = useRef<Terminal | null>(null);
  const wsRef = useRef<WebSocket | null>(null);
  const [menu, setMenu] = useState<{ x: number; y: number } | null>(null);
  const path = fullPath ?? `/p/${projectId}${wsPath}`;

  useEffect(() => {
    const term = new Terminal({
      cursorBlink: true,
      fontFamily: "ui-monospace, SFMono-Regular, Menlo, Consolas, monospace",
      fontSize: 13,
      theme: { background: "#0B0E14" },
      scrollback: 10000,
    });
    termRef.current = term;
    const fit = new FitAddon();
    term.loadAddon(fit);
    if (host.current) {
      term.open(host.current);
      // Only fit if the pane already has real geometry. Fitting a 0×0 (hidden or
      // not-yet-laid-out) pane produces a garbage 1-col grid that tmux redraws into
      // — the "…" placeholder dots seen while loading. The rAF fit below handles
      // the real size once layout settles.
      if (host.current.clientWidth > 0 && host.current.clientHeight > 0) fit.fit();
    }

    const ws = new WebSocket(wsURL(path));
    wsRef.current = ws;
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
    //   • plain Enter                → CR (\r): submit
    //   • Shift/Cmd/Ctrl/Alt+Enter   → ESC+CR (\x1b\r): the meta-return sequence
    //     apps read as "insert a newline, don't submit" (Shift+Enter is the usual
    //     newline key in Claude's composer and most chat inputs)
    // Returning false stops xterm's own default so we don't double-send.
    term.attachCustomKeyEventHandler((e) => {
      if (e.type !== "keydown" || e.key !== "Enter") return true;
      // preventDefault + returning false stops xterm from ALSO emitting its own
      // \r for this key (which double-submitted).
      e.preventDefault();
      sendBytes(e.shiftKey || e.metaKey || e.ctrlKey || e.altKey ? "\x1b\r" : "\r");
      return false;
    });

    const doFit = () => {
      // Only fit when the pane actually has a size. A hidden tab (display:none) or
      // a not-yet-laid-out pane reports 0×0; fitting then and again on show is the
      // visible resize flash (FOUC). Skip until there's real geometry.
      const el = host.current;
      if (!el || el.clientWidth === 0 || el.clientHeight === 0) return;
      // Skip if the fit wouldn't change anything — repeatedly calling fit.fit()
      // with the same result still triggers a reflow, which is the "flicker in and
      // out" when the settle-fits re-run. Only apply when dims actually differ.
      const dims = fit.proposeDimensions();
      if (dims && dims.cols === term.cols && dims.rows === term.rows) return;
      fit.fit();
      sendResize();
    };

    let resizeTimer: number;
    const onResize = () => {
      window.clearTimeout(resizeTimer);
      resizeTimer = window.setTimeout(doFit, 100);
    };
    window.addEventListener("resize", onResize);

    // Refit when the PANE's own box changes size — the window OR the tab becoming
    // visible / a resize-handle drag. The 0×0 guard in doFit means the hidden→shown
    // transition fits exactly once, at the final size, instead of flashing.
    const ro = new ResizeObserver(() => onResize());
    if (host.current) ro.observe(host.current);

    // Fit as soon as the pane has real geometry. A single rAF isn't enough: if the
    // pane mounts hidden or mid-animation it's 0×0 for the first frame(s), the fit
    // is skipped, xterm keeps its default 80×24 grid, and it never sends a resize
    // — so tmux stays at the attach size and the extra space fills with dots.
    // Retry on rAF until doFit actually fits (up to ~1.5s), then stop.
    let rafId = 0;
    let tries = 0;
    const settleTimers: number[] = [];
    const fitWhenReady = () => {
      const el = host.current;
      const ready = el && el.clientWidth > 0 && el.clientHeight > 0;
      if (ready) {
        doFit();
        // Re-fit a few times after layout settles. On mount the pane can report a
        // transient (narrow) size — xterm fits to it, sends that resize, and tmux
        // pins its window there; nothing later shrinks/grows so it stays, leaving
        // dotted fill. These trailing fits re-measure once the real size lands.
        [120, 350, 700].forEach((ms) => settleTimers.push(window.setTimeout(doFit, ms)));
        return;
      }
      if (tries++ < 90) rafId = requestAnimationFrame(fitWhenReady);
    };
    rafId = requestAnimationFrame(fitWhenReady);

    // Re-fit once web fonts are ready. FitAddon measures a character cell to
    // compute cols/rows; if it runs before the monospace font loads it measures a
    // fallback with a wider cell → far too few cols (e.g. 59 instead of ~180),
    // leaving dotted fill. document.fonts.ready guarantees a correct re-measure.
    if (document.fonts && document.fonts.ready) {
      document.fonts.ready.then(() => doFit()).catch(() => {});
    }

    return () => {
      window.removeEventListener("resize", onResize);
      ro.disconnect();
      window.clearTimeout(resizeTimer);
      settleTimers.forEach((t) => window.clearTimeout(t));
      cancelAnimationFrame(rafId);
      try {
        ws.close();
      } catch {
        /* ignore */
      }
      term.dispose();
    };
  }, [path]);

  // Run a context-menu action. tmux ops (split/close/clear) go to the backend for
  // the session; copy/paste/clear-screen act on the xterm client-side.
  const runAction = async (action: TermAction) => {
    setMenu(null);
    const term = termRef.current;
    switch (action) {
      case "copy": {
        const sel = term?.getSelection();
        if (sel) await navigator.clipboard.writeText(sel).catch(() => {});
        return;
      }
      case "paste": {
        const text = await navigator.clipboard.readText().catch(() => "");
        if (text && wsRef.current?.readyState === WebSocket.OPEN) {
          wsRef.current.send(new TextEncoder().encode(text));
        }
        return;
      }
      case "clear":
        // For tmux terminals, send `clear` so scrollback clears server-side too;
        // otherwise just clear the xterm view.
        if (kind === "claude" || kind === "host") {
          await postJSON(`/p/${projectId}/terminal/action`, { kind, action: "clear" }).catch(() => {});
        } else {
          term?.clear();
        }
        return;
      case "split-h":
      case "split-v":
      case "kill-pane":
        if (kind === "claude" || kind === "host") {
          await postJSON(`/p/${projectId}/terminal/action`, { kind, action }).catch(() => {});
        }
        return;
    }
  };

  // Only the tmux-backed terminals can split/close. projectId is required for any
  // backend action.
  const canSplit = (kind === "claude" || kind === "host") && !!projectId;

  return (
    <div
      className="term-fill"
      ref={host}
      style={{ width: "100%", height: "100%" }}
      onContextMenu={(e) => {
        e.preventDefault();
        setMenu({ x: e.clientX, y: e.clientY });
      }}
    >
      {menu && (
        <TerminalMenu x={menu.x} y={menu.y} canSplit={canSplit} onClose={() => setMenu(null)} onAction={runAction} />
      )}
    </div>
  );
}
