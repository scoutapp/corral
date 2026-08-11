import { useState } from "react";
import type { UpdateStatus } from "../api/types";
import { Modal } from "./Modal";
import { XtermPane } from "./XtermPane";

// A slim banner shown at the top of every page: either "update available" or,
// when the source can't be reached, a muted "couldn't check" notice. Both are
// dismissible, and the dismissal PERSISTS across reloads/navigation via
// localStorage:
//   - the unreachable notice stays dismissed until it becomes reachable again
//     (dismissing clears once a real answer arrives, so a later update still
//     surfaces);
//   - the update banner is dismissed PER VERSION, so a newer release re-shows it.
// "Update…" opens a terminal running `corral update` on the HOST (a real PTY,
// so sudo/confirm prompts work) — the same consent model as the host shell.

const UNREACHABLE_KEY = "corral.update.unreachableDismissed";
const VERSION_KEY = "corral.update.dismissedVersion";

function lsGet(key: string): string {
  try {
    return localStorage.getItem(key) || "";
  } catch {
    return "";
  }
}
function lsSet(key: string, val: string) {
  try {
    localStorage.setItem(key, val);
  } catch {
    /* ignore */
  }
}

export function UpdateBanner({ status }: { status: UpdateStatus | null }) {
  // Seed dismissal state from localStorage so a persisted dismissal hides the
  // banner immediately on mount (no flash).
  const [unreachableDismissed, setUnreachableDismissed] = useState(() => lsGet(UNREACHABLE_KEY) === "1");
  const [dismissedVersion, setDismissedVersion] = useState(() => lsGet(VERSION_KEY));
  const [running, setRunning] = useState(false);

  if (!status) return null;

  // Couldn't reach the update source (e.g. a still-private repo, or offline):
  // show a muted, dismissible notice rather than an update prompt.
  if (status.unreachable) {
    if (unreachableDismissed) return null;
    const dismiss = () => {
      lsSet(UNREACHABLE_KEY, "1");
      setUnreachableDismissed(true);
    };
    return (
      <div className="update-banner update-banner-warn" role="status">
        <span className="update-banner-text">
          Couldn't check for updates — the update source{" "}
          <span className="muted">({status.repo || "unknown"})</span> isn't reachable right now.
        </span>
        <span className="update-banner-actions">
          <button className="update-banner-dismiss" title="Dismiss" onClick={dismiss}>
            ✕
          </button>
        </span>
      </div>
    );
  }

  // The source is reachable again — clear any persisted "unreachable" dismissal
  // so a future outage shows the notice afresh.
  if (unreachableDismissed) {
    lsSet(UNREACHABLE_KEY, "");
    setUnreachableDismissed(false);
  }

  if (!status.update_available) return null;
  // Dismissed for THIS exact release? Stay hidden until a newer one appears.
  if (dismissedVersion && dismissedVersion === status.latest) return null;

  const dismiss = () => {
    lsSet(VERSION_KEY, status.latest);
    setDismissedVersion(status.latest);
  };

  return (
    <>
      <div className="update-banner" role="status">
        <span className="update-banner-text">
          A new corral release is available — <strong>{status.latest}</strong>{" "}
          <span className="muted">(you have {status.current})</span>
        </span>
        <span className="update-banner-actions">
          <button className="update-banner-btn" onClick={() => setRunning(true)}>
            Update…
          </button>
          <button className="update-banner-dismiss" title="Dismiss" onClick={dismiss}>
            ✕
          </button>
        </span>
      </div>

      {running && (
        <Modal title={`Update corral → ${status.latest}`} onClose={() => setRunning(false)}>
          <p className="muted" style={{ marginTop: 0 }}>
            Running <code>corral update</code> on the host. If your binary lives somewhere that needs
            elevated permissions, follow any printed <code>sudo</code> instruction in the terminal. Restart the
            dashboard afterward to pick up the new build.
          </p>
          <div className="populate-term" style={{ display: "block", height: 380 }}>
            <div className="screen-bar">
              <i className="screen-dot" />
              corral update
            </div>
            <XtermPane fullPath="/update/ws" />
          </div>
        </Modal>
      )}
    </>
  );
}
