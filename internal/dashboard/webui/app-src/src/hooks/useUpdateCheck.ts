import { useEffect, useState } from "react";
import { getJSON } from "../api/client";
import type { UpdateStatus } from "../api/types";

// Polls /update-status so the app can show an "update available" banner. The
// backend caches the latest tag for ~24h and only then re-hits GitHub, so it's
// cheap to call often; we poll on mount and every CLIENT_INTERVAL after that.
// Mounted once from the persistent App (not per page), so a tab left open for
// days keeps checking without a manual refresh — the backend cache throttles the
// actual network calls.

const CLIENT_INTERVAL = 6 * 60 * 60 * 1000; // 6h

export function useUpdateCheck(): UpdateStatus | null {
  const [status, setStatus] = useState<UpdateStatus | null>(null);

  useEffect(() => {
    let alive = true;
    const check = () => {
      getJSON<UpdateStatus>("/update-status")
        .then((s) => {
          if (alive) setStatus(s);
        })
        .catch(() => {
          /* non-fatal: no banner rather than an error */
        });
    };
    check();
    const id = window.setInterval(check, CLIENT_INTERVAL);
    return () => {
      alive = false;
      window.clearInterval(id);
    };
  }, []);

  return status;
}
