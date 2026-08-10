import { useCallback, useState } from "react";

// usePersistentState is useState backed by localStorage under `key`, so a value
// survives component unmount/remount (e.g. navigating away from a project page
// and back) and full page reloads. JSON-encoded; a missing/corrupt entry falls
// back to `initial`. localStorage access is wrapped so a privacy-mode failure
// degrades to plain in-memory state rather than throwing.
export function usePersistentState<T>(key: string, initial: T): [T, (v: T | ((prev: T) => T)) => void] {
  const [value, setValue] = useState<T>(() => {
    try {
      const raw = localStorage.getItem(key);
      if (raw != null) return JSON.parse(raw) as T;
    } catch {
      /* ignore */
    }
    return initial;
  });

  const set = useCallback(
    (v: T | ((prev: T) => T)) => {
      setValue((prev) => {
        const next = typeof v === "function" ? (v as (p: T) => T)(prev) : v;
        try {
          localStorage.setItem(key, JSON.stringify(next));
        } catch {
          /* ignore */
        }
        return next;
      });
    },
    [key],
  );

  return [value, set];
}
