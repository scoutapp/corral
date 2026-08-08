import { useEffect, useRef, useState } from "react";
import { Terminal } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import { getJSON, wsURL } from "../api/client";
import type { SSHKeysStatus } from "../api/types";

// Shared SSH-key load modal. Opens a terminal-in-a-modal bridged to
// /p/<id>/sshkeys/ws, which runs `ssh-add` for the project's scoped agent so a
// passphrase prompt appears in a real PTY. Keys never leave the PTY; the
// passphrase is typed into the terminal and read by ssh-add directly.
//
// onDone(loaded) fires when the modal closes: loaded is best-effort true if the
// agent reported all keys loaded (polled via /sshkeys/status). Ported from
// ssh-load.js; used by the landing-page Start button and the Config tab.

export function SSHLoadModal({ projectId, onDone }: { projectId: string; onDone: (loaded: boolean) => void }) {
  const termHost = useRef<HTMLDivElement | null>(null);
  const [loadedSeen, setLoadedSeen] = useState(false);
  const doneRef = useRef(false);
  const wsRef = useRef<WebSocket | null>(null);
  const termRef = useRef<Terminal | null>(null);
  const loadedSeenRef = useRef(false);

  const finish = (loaded: boolean) => {
    if (doneRef.current) return;
    doneRef.current = true;
    try {
      wsRef.current?.close();
    } catch {
      /* ignore */
    }
    try {
      termRef.current?.dispose();
    } catch {
      /* ignore */
    }
    onDone(loaded);
  };

  useEffect(() => {
    const term = new Terminal({
      cursorBlink: true,
      fontFamily: "ui-monospace, SFMono-Regular, Menlo, Consolas, monospace",
      fontSize: 13,
      theme: { background: "#0B0E14" },
      scrollback: 1000,
    });
    termRef.current = term;
    const fit = new FitAddon();
    term.loadAddon(fit);
    if (termHost.current) {
      term.open(termHost.current);
      fit.fit();
    }

    const ws = new WebSocket(wsURL(`/p/${encodeURIComponent(projectId)}/sshkeys/ws`));
    wsRef.current = ws;
    ws.binaryType = "arraybuffer";
    const decoder = new TextDecoder();
    ws.onopen = () => {
      ws.send(JSON.stringify({ type: "resize", cols: term.cols, rows: term.rows }));
      term.focus();
    };
    ws.onmessage = (ev) => {
      term.write(typeof ev.data === "string" ? ev.data : decoder.decode(new Uint8Array(ev.data)));
    };
    ws.onclose = () => finish(loadedSeenRef.current);
    term.onData((d) => {
      if (ws.readyState === WebSocket.OPEN) ws.send(new TextEncoder().encode(d));
    });

    // The PTY runs a persistent interactive shell (ssh-add, then a live prompt),
    // so the WS does NOT close when ssh-add finishes. Poll the load status; once
    // all keys are loaded, reveal a highlighted Continue button.
    const poll = window.setInterval(async () => {
      try {
        const s = await getJSON<SSHKeysStatus>(`/p/${encodeURIComponent(projectId)}/sshkeys/status`);
        if (s && s.loaded && !loadedSeenRef.current) {
          loadedSeenRef.current = true;
          setLoadedSeen(true);
          term.write("\r\n\x1b[32m✓ keys loaded — click Continue to proceed\x1b[0m\r\n");
        }
      } catch {
        /* transient */
      }
    }, 1500);

    return () => {
      window.clearInterval(poll);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [projectId]);

  return (
    <div className="sshload-overlay" onClick={(e) => e.target === e.currentTarget && finish(loadedSeenRef.current)}>
      <div className="sshload-box">
        <h3>
          Load SSH keys <span className="sshload-hostbadge">host shell</span>
        </h3>
        <p className="muted">
          This is a real shell <strong>on your host machine</strong> (not sandboxed). It runs <code>ssh-add</code> for
          this project's scoped agent — type each key's passphrase at the prompt. The passphrase goes straight to{" "}
          <code>ssh-add</code> in the terminal; it's never stored or sent as data.
        </p>
        <div className="sshload-term" ref={termHost} />
        <div className="sshload-actions">
          {loadedSeen ? (
            <button className="cfg-btn sshload-continue" onClick={() => finish(true)}>
              Continue ▶
            </button>
          ) : (
            <button className="cfg-btn cfg-btn-ghost sshload-done" onClick={() => finish(loadedSeenRef.current)}>
              Cancel
            </button>
          )}
        </div>
      </div>
    </div>
  );
}
