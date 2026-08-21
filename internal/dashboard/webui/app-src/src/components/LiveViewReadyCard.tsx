import type { LiveViewReady } from "../lib/liveViewReady";

// LiveViewReadyCard renders a worker's verified "=== LIVE VIEW READY ===" block
// as a green-bordered success callout — the visible signal that an app was booted
// AND its login was verified through the Live View proxy. Only a status of
// "verified" gets the green treatment; anything else renders neutral (the worker
// shouldn't emit the block unless it verified, but we don't fake certainty).
export function LiveViewReadyCard({ data }: { data: LiveViewReady }) {
  const verified = data.status.toLowerCase() === "verified";
  return (
    <div className={`lvr-card${verified ? " verified" : ""}`}>
      <div className="lvr-head">
        <span className="lvr-badge">{verified ? "✓ Live View ready" : `Live View: ${data.status || "unknown"}`}</span>
        {data.port && <span className="lvr-port">port {data.port}</span>}
      </div>
      {data.note && <div className="lvr-note">{data.note}</div>}
      <div className="lvr-fields">
        {data.urlPath && (
          <div className="lvr-field">
            <span className="lvr-key">page</span>
            <code className="lvr-val">{data.urlPath}</code>
          </div>
        )}
        {data.login && (
          <div className="lvr-field">
            <span className="lvr-key">sign in</span>
            <code className="lvr-val">{data.login}</code>
          </div>
        )}
      </div>
    </div>
  );
}
