import { useCallback, useEffect, useRef, useState } from "react";

// Per-project + global mute state, persisted in localStorage (survives reloads).
// Cleared when the server boot_id changes, since project ids can be reused
// across daemon restarts and a stale mute must not stick to an unrelated project.
const MUTES_KEY = "sandclaude.muted"; // { "<projectId>": true }
const GLOBAL_KEY = "sandclaude.mutedAll"; // "1" when everything is muted
const BOOT_KEY = "sandclaude.bootId"; // last server boot id we saw

function loadMutes(): Record<string, boolean> {
  try {
    return JSON.parse(localStorage.getItem(MUTES_KEY) || "{}") || {};
  } catch {
    return {};
  }
}

export function useMutes(bootId: string | null) {
  const [muted, setMuted] = useState<Record<string, boolean>>(loadMutes);
  const [mutedAll, setMutedAll] = useState<boolean>(() => localStorage.getItem(GLOBAL_KEY) === "1");
  const lastBoot = useRef<string | null>(null);

  // Reconcile boot: on a new/changed boot_id, wipe per-project mutes.
  useEffect(() => {
    if (!bootId) return;
    let seen: string | null = null;
    try {
      seen = localStorage.getItem(BOOT_KEY);
    } catch {
      /* ignore */
    }
    if (seen === bootId) {
      lastBoot.current = bootId;
      return;
    }
    setMuted({});
    try {
      localStorage.setItem(MUTES_KEY, "{}");
      localStorage.setItem(BOOT_KEY, bootId);
    } catch {
      /* ignore */
    }
    lastBoot.current = bootId;
  }, [bootId]);

  const isMuted = useCallback((id: string) => mutedAll || !!muted[id], [mutedAll, muted]);

  const toggleMute = useCallback((id: string) => {
    setMuted((m) => {
      const next = { ...m };
      if (next[id]) delete next[id];
      else next[id] = true;
      try {
        localStorage.setItem(MUTES_KEY, JSON.stringify(next));
      } catch {
        /* ignore */
      }
      return next;
    });
  }, []);

  const toggleMuteAll = useCallback(() => {
    setMutedAll((v) => {
      const next = !v;
      try {
        localStorage.setItem(GLOBAL_KEY, next ? "1" : "0");
      } catch {
        /* ignore */
      }
      return next;
    });
  }, []);

  const forgetMute = useCallback((id: string) => {
    setMuted((m) => {
      if (!m[id]) return m;
      const next = { ...m };
      delete next[id];
      try {
        localStorage.setItem(MUTES_KEY, JSON.stringify(next));
      } catch {
        /* ignore */
      }
      return next;
    });
  }, []);

  return { isMuted, mutedAll, toggleMute, toggleMuteAll, forgetMute };
}
