import { useCallback, useEffect, useRef, useState } from "react";
import { getJSON } from "../api/client";

// Live View tab (#6): watch a web app the sandbox is running, embedded via an
// iframe. The app usually runs inside DinD; the dashboard tunnels into the
// container to reach it (see internal/dashboard/liveview.go). Discovery
// (/live-ports) lists the container's listening ports as quick-picks; a free-text
// box covers anything discovery misses.
//
// ISOLATION: the app is UNTRUSTED. The iframe is served from a SEPARATE origin —
// http://localhost:<livePort> (a second loopback listener), NOT the dashboard's
// http://127.0.0.1:<dashPort>. We fetch that origin + a one-time token from
// /api/live-origin and point the iframe there.
//
// We use allow-same-origin so the app keeps a real origin (cookies persist →
// login sticks → CSS/JS render). This is SAFE here precisely because that origin
// is localhost:<livePort>, not the dashboard: the app's JS can reach ITS OWN
// origin only, and the dashboard's HttpOnly corral_dash_token (host-only to
// 127.0.0.1) is never sent to localhost. So the framed app cannot call dashboard
// APIs or read dashboard cookies/DOM. Per-project sessions are isolated by
// server-side cookie Path-scoping (hardenLiveResponse). See liveview_server.go.
const LIVE_IFRAME_SANDBOX = "allow-scripts allow-forms allow-popups allow-modals allow-same-origin";

interface LiveOrigin {
  base_url: string;
  token: string;
}

interface PortsResp {
  ports: number[];
  container_up: boolean;
}

export function LiveViewTab({ projectId, containerUp }: { projectId: string; containerUp: boolean }) {
  const [ports, setPorts] = useState<number[]>([]);
  const [port, setPort] = useState<number | null>(null);
  const [input, setInput] = useState("");
  // The path under the port to open (for apps served under a sub-path, e.g.
  // "/docs/node/" where "/" 404s). "" = the app root.
  const [path, setPath] = useState("");
  const [pathInput, setPathInput] = useState("");
  // Bumped to force the iframe to reload the current port.
  const [reloadKey, setReloadKey] = useState(0);
  // The distinct origin (localhost:<livePort>) + token the iframe loads from,
  // fetched once from the dashboard (same-origin, cookie-authed).
  const [liveOrigin, setLiveOrigin] = useState<LiveOrigin | null>(null);
  useEffect(() => {
    getJSON<LiveOrigin>("/api/live-origin")
      .then(setLiveOrigin)
      .catch(() => setLiveOrigin(null));
  }, []);

  // Whether the user has manually chosen a port this session. Once they have, we
  // stop overriding their choice with the stored/discovered default.
  const userPicked = useRef(false);

  const refreshPorts = useCallback(() => {
    getJSON<PortsResp>(`/p/${projectId}/live-ports`)
      .then((r) => {
        setPorts(r.ports || []);
        // Auto-select the first discovered port if nothing is chosen yet.
        setPort((cur) => (cur == null && !userPicked.current && r.ports && r.ports.length ? r.ports[0] : cur));
      })
      .catch(() => setPorts([]));
  }, [projectId]);

  // The stored preferred port (the host Claude sets it after starting a web app,
  // via PUT /p/<id>/live-port). It takes priority over first-discovered. Poll it
  // so if Claude sets it while the tab is open, the view moves there — unless the
  // user has manually picked a port, in which case we leave their choice alone.
  const refreshPreferred = useCallback(() => {
    getJSON<{ port: number; path?: string }>(`/p/${projectId}/live-port`)
      .then((r) => {
        if (r.port && !userPicked.current) {
          setPort((cur) => (cur == null || cur !== r.port ? r.port : cur));
          const pth = r.path || "";
          setPath((cur) => (cur !== pth ? pth : cur));
          setPathInput((cur) => (cur === "" ? pth : cur));
        }
      })
      .catch(() => {});
  }, [projectId]);

  useEffect(() => {
    if (!containerUp) return;
    refreshPorts();
    refreshPreferred();
    const t = setInterval(refreshPreferred, 4000);
    return () => clearInterval(t);
  }, [containerUp, refreshPorts, refreshPreferred]);

  const go = (p: number) => {
    userPicked.current = true;
    setPort(p);
    setReloadKey((k) => k + 1);
  };
  const goInput = () => {
    const p = parseInt(input.trim(), 10);
    if (p >= 1 && p <= 65535) go(p);
  };

  // The iframe src: an ABSOLUTE URL on the separate live origin
  // (http://localhost:<livePort>/p/<id>/live/<port><path>), carrying the one-time
  // ?__live_token= that the live listener swaps for a localhost cookie and strips
  // via a redirect. Empty until we have both a port and the live origin.
  const src =
    port != null && liveOrigin
      ? `${liveOrigin.base_url}/p/${projectId}/live/${port}${path || "/"}?__live_token=${encodeURIComponent(liveOrigin.token)}`
      : "";

  const applyPath = () => {
    setPath(pathInput.trim());
    setReloadKey((k) => k + 1);
  };

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
      <div className="live-hint muted">
        Pick a port and path above, or ask the <strong>Ask Claude</strong> host chat
        (the global one — it can run <code>corral api</code>) to start your app and
        set the Live View port for you.
      </div>
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
        <span className="live-label live-path-label">Path</span>
        <input
          className="live-port-input live-path-input"
          placeholder="/"
          value={pathInput}
          onChange={(e) => setPathInput(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter") applyPath();
          }}
          title="Open the app at this path (e.g. /docs/node/)"
        />
        <button className="live-btn" onClick={applyPath} title="Open this path">
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
