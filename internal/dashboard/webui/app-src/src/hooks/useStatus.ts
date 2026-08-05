import { useEffect, useRef, useState } from "react";
import { getJSON } from "../api/client";
import type { StatusResponse, StatusRow } from "../api/types";

const POLL_MS = 4000;

export interface StatusState {
  projects: StatusRow[];
  bootId: string | null;
  connected: boolean;
}

// useStatus polls GET /status on an interval and returns the latest project
// rows. It's the single source the landing page and the cross-project toasts
// both read from. On a boot_id change (daemon restart) callers may want to
// reset edge memory — bootId is exposed so they can detect it.
export function useStatus(pollMs: number = POLL_MS): StatusState {
  const [state, setState] = useState<StatusState>({ projects: [], bootId: null, connected: false });
  const timer = useRef<number | null>(null);

  useEffect(() => {
    let alive = true;
    const poll = async () => {
      try {
        const data = await getJSON<StatusResponse>("/status");
        if (!alive) return;
        setState({ projects: data.projects || [], bootId: data.boot_id, connected: true });
      } catch {
        if (!alive) return;
        setState((s) => ({ ...s, connected: false }));
      }
    };
    poll();
    timer.current = window.setInterval(poll, pollMs);
    return () => {
      alive = false;
      if (timer.current) window.clearInterval(timer.current);
    };
  }, [pollMs]);

  return state;
}
