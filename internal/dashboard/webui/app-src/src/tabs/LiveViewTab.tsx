import { useCallback, useEffect, useState } from "react";
import { getJSON } from "../api/client";

// Live View tab (#6): watch a web app the sandbox is running, embedded via an
// iframe served by the dashboard's reverse-proxy at /p/<id>/live/<port>/.
//
// The app usually runs inside DinD; the dashboard tunnels into the container to
// reach it (see internal/dashboard/liveview.go). Discovery (/live-ports) lists
// the container's listening ports as quick-picks; a free-text box covers
// anything discovery misses.
//
// ISOLATION: the app is UNTRUSTED, so the iframe is sandboxed. We deliberately
// OMIT allow-same-origin: without it the framed content runs in an opaque origin
// and cannot read the dashboard's cookies, DOM, localStorage, or make
// same-origin requests to dashboard APIs — so embedding it can't become a
// sandbox→dashboard escalation through the browser. We DO allow scripts/forms/
// popups so real apps still work. The server half (frame-ancestors CSP, stripped
// Set-Cookie/X-Frame-Options) lives in liveview.go's hardenLiveResponse.
const LIVE_IFRAME_SANDBOX = "allow-scripts allow-forms allow-popups allow-modals";

interface PortsResp {
  ports: number[];
  container_up: boolean;
}

export function LiveViewTab({ projectId, containerUp }: { projectId: string; containerUp: boolean }) {
  const [ports, setPorts] = useState<number[]>([]);
  const [port, setPort] = useState<number | null>(null);
  const [input, setInput] = useState("");
  // Bumped to force the iframe to reload the current port.
  const [reloadKey, setReloadKey] = useState(0);

  const refreshPorts = useCallback(() => {
    getJSON<PortsResp>(`/p/${projectId}/live-ports`)
      .then((r) => {
        setPorts(r.ports || []);
        // Auto-select the first discovered port if nothing is chosen yet.
        setPort((cur) => (cur == null && r.ports && r.ports.length ? r.ports[0] : cur));
      })
      .catch(() => setPorts([]));
  }, [projectId]);

  useEffect(() => {
    if (containerUp) refreshPorts();
  }, [containerUp, refreshPorts]);

  const go = (p: number) => {
    setPort(p);
    setReloadKey((k) => k + 1);
  };
  const goInput = () => {
    const p = parseInt(input.trim(), 10);
    if (p >= 1 && p <= 65535) go(p);
  };

  const src = port != null ? `/p/${projectId}/live/${port}/` : "";

  if (!containerUp) {
    return (
      <div className="live-empty">
        <p>The project container isn’t running.</p>
        <p className="muted">Start the project, then run your app to view it here.</p>
      </div>
    );
  }

  return (
    <div className="live-view">
      <div className="live-bar">
        <span className="live-label">Port</span>
        {ports.map((p) => (
          <button key={p} className={`live-port${p === port ? " active" : ""}`} onClick={() => go(p)}>
            {p}
          </button>
        ))}
        <input
          className="live-port-input"
          placeholder="port"
          inputMode="numeric"
          value={input}
          onChange={(e) => setInput(e.target.value.replace(/[^0-9]/g, ""))}
          onKeyDown={(e) => {
            if (e.key === "Enter") goInput();
          }}
        />
        <button className="live-btn" onClick={goInput} disabled={!input.trim()}>
          Go
        </button>
        <span className="live-spacer" />
        <button className="live-btn" onClick={refreshPorts} title="Re-scan listening ports">
          ⟳ Ports
        </button>
        {port != null && (
          <>
            <button className="live-btn" onClick={() => setReloadKey((k) => k + 1)} title="Reload the view">
              ⟳ Reload
            </button>
            <a className="live-btn" href={src} target="_blank" rel="noreferrer" title="Open in a new tab">
              ↗ Open
            </a>
          </>
        )}
      </div>

      {port == null ? (
        <div className="live-empty">
          <p>No port selected.</p>
          {ports.length === 0 && (
            <p className="muted">
              No listening ports detected. Start your app’s container with{" "}
              <code>-p 3000:3000</code> (or bind it in the outer container) to view it here, then ⟳ Ports.
            </p>
          )}
        </div>
      ) : (
        <div className="live-frame-wrap">
          <iframe
            key={reloadKey}
            className="live-frame"
            src={src}
            title={`live view on port ${port}`}
            sandbox={LIVE_IFRAME_SANDBOX}
          />
        </div>
      )}
    </div>
  );
}
