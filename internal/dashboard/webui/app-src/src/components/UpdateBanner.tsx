import { useState } from "react";
import type { UpdateStatus } from "../api/types";
import { Modal } from "./Modal";
import { XtermPane } from "./XtermPane";

// A slim, dismissible banner shown at the top of every page when a newer
// sandclaude release is available. "Update" opens a terminal that runs
// `sandclaude update` on the HOST (a real PTY, so sudo/confirm prompts work and
// output is visible) — the same consent model as the host shell. Dismissing
// hides it for this tab session; it reappears on reload if still out of date.
export function UpdateBanner({ status }: { status: UpdateStatus | null }) {
  const [dismissed, setDismissed] = useState(false);
  const [running, setRunning] = useState(false);

  if (!status || dismissed) return null;

  // Couldn't reach the update source (e.g. a still-private repo, or offline):
  // show a muted, dismissible notice rather than an update prompt.
  if (status.unreachable) {
    return (
      <div className="update-banner update-banner-warn" role="status">
        <span className="update-banner-text">
          Couldn't check for updates — the update source{" "}
          <span className="muted">({status.repo || "unknown"})</span> isn't reachable right now.
        </span>
        <span className="update-banner-actions">
          <button className="update-banner-dismiss" title="Dismiss" onClick={() => setDismissed(true)}>
            ✕
          </button>
        </span>
      </div>
    );
  }

  if (!status.update_available) return null;

  return (
    <>
      <div className="update-banner" role="status">
        <span className="update-banner-text">
          A new sandclaude release is available — <strong>{status.latest}</strong>{" "}
          <span className="muted">(you have {status.current})</span>
        </span>
        <span className="update-banner-actions">
          <button className="update-banner-btn" onClick={() => setRunning(true)}>
            Update…
          </button>
          <button className="update-banner-dismiss" title="Dismiss" onClick={() => setDismissed(true)}>
            ✕
          </button>
        </span>
      </div>

      {running && (
        <Modal title={`Update sandclaude → ${status.latest}`} onClose={() => setRunning(false)}>
          <p className="muted" style={{ marginTop: 0 }}>
            Running <code>sandclaude update</code> on the host. If your binary lives somewhere that needs
            elevated permissions, follow any printed <code>sudo</code> instruction in the terminal. Restart the
            dashboard afterward to pick up the new build.
          </p>
          <div className="populate-term" style={{ display: "block", height: 380 }}>
            <div className="screen-bar">
              <i className="screen-dot" />
              sandclaude update
            </div>
            <XtermPane fullPath="/update/ws" />
          </div>
        </Modal>
      )}
    </>
  );
}
