import { useCallback, useEffect, useState } from "react";

// Do Not Disturb for notifications (chime + toasts). DEFAULT ON. State lives in
// localStorage so it's per-browser and uses the browser's LOCAL time to decide
// quiet hours — outside the configured window (default 9am–5pm) notifications
// are suppressed. A temporary "snooze" (allow-for-a-while) overrides DND.
//
// Notifications are ALLOWED when: DND disabled, OR the current local hour is
// inside [startHour, endHour), OR a snooze is active.

const DND_KEY = "corral.dnd"; // { enabled, startHour, endHour }
const SNOOZE_KEY = "corral.dnd.snoozeUntil"; // epoch ms, or "inf"

export interface DndConfig {
  enabled: boolean;
  startHour: number; // 0-23, inclusive
  endHour: number; // 0-23, exclusive
}

const DEFAULTS: DndConfig = { enabled: true, startHour: 9, endHour: 17 };

function loadConfig(): DndConfig {
  try {
    const raw = JSON.parse(localStorage.getItem(DND_KEY) || "null");
    if (raw && typeof raw.enabled === "boolean") {
      return {
        enabled: raw.enabled,
        startHour: clampHour(raw.startHour, DEFAULTS.startHour),
        endHour: clampHour(raw.endHour, DEFAULTS.endHour),
      };
    }
  } catch {
    /* ignore */
  }
  return DEFAULTS;
}

function clampHour(v: unknown, fallback: number): number {
  const n = Number(v);
  return Number.isInteger(n) && n >= 0 && n <= 24 ? n : fallback;
}

function loadSnooze(): number | "inf" | null {
  try {
    const raw = localStorage.getItem(SNOOZE_KEY);
    if (raw === "inf") return "inf";
    const n = Number(raw);
    return n > 0 ? n : null;
  } catch {
    return null;
  }
}

// insideWindow reports whether hour is within [start, end). Handles a window
// that wraps past midnight (e.g. 22–6) too, for flexibility.
function insideWindow(hour: number, start: number, end: number): boolean {
  if (start === end) return false; // empty window → always quiet
  if (start < end) return hour >= start && hour < end;
  return hour >= start || hour < end; // wraps midnight
}

export function useDnd() {
  const [config, setConfig] = useState<DndConfig>(loadConfig);
  const [snoozeUntil, setSnoozeUntil] = useState<number | "inf" | null>(loadSnooze);
  // A ticking "now" so the moon/quiet state re-evaluates as time passes and as
  // a snooze expires (every 30s is plenty for hour-granularity quiet windows).
  const [now, setNow] = useState<number>(() => Date.now());
  useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), 30_000);
    return () => clearInterval(t);
  }, []);

  // Persist config + snooze.
  useEffect(() => {
    try {
      localStorage.setItem(DND_KEY, JSON.stringify(config));
    } catch {
      /* ignore */
    }
  }, [config]);
  useEffect(() => {
    try {
      if (snoozeUntil == null) localStorage.removeItem(SNOOZE_KEY);
      else localStorage.setItem(SNOOZE_KEY, snoozeUntil === "inf" ? "inf" : String(snoozeUntil));
    } catch {
      /* ignore */
    }
  }, [snoozeUntil]);

  // Is a snooze currently active? Auto-clear an expired one.
  const snoozeActive = snoozeUntil === "inf" || (typeof snoozeUntil === "number" && snoozeUntil > now);
  useEffect(() => {
    if (typeof snoozeUntil === "number" && snoozeUntil <= now) setSnoozeUntil(null);
  }, [snoozeUntil, now]);

  // Quiet = DND on, outside the window, and not snoozed.
  const localHour = new Date(now).getHours();
  const inWindow = insideWindow(localHour, config.startHour, config.endHour);
  const quiet = config.enabled && !inWindow && !snoozeActive;

  const notificationsAllowed = useCallback(() => !quiet, [quiet]);

  // Snooze controls (temporary allow).
  const snoozeFor = useCallback((minutes: number) => setSnoozeUntil(Date.now() + minutes * 60_000), []);
  const snoozeUntilOff = useCallback(() => setSnoozeUntil("inf"), []);
  const clearSnooze = useCallback(() => setSnoozeUntil(null), []);

  // Config controls.
  const setEnabled = useCallback((enabled: boolean) => setConfig((c) => ({ ...c, enabled })), []);
  const setWindow = useCallback(
    (startHour: number, endHour: number) => setConfig((c) => ({ ...c, startHour, endHour })),
    [],
  );

  return {
    config,
    quiet, // true when notifications are currently suppressed
    snoozeActive,
    snoozeUntil,
    notificationsAllowed,
    snoozeFor,
    snoozeUntilOff,
    clearSnooze,
    setEnabled,
    setWindow,
  };
}
