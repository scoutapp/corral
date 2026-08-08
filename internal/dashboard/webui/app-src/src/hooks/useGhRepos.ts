import { useEffect, useState } from "react";
import { getJSON } from "../api/client";
import type { GhRepo } from "../api/types";

// Module-level cache so the gh repo list (a slow `gh repo list`) is fetched once
// and shared across every modal that opens, matching projects-ui.js's ghReposCache.
let cache: GhRepo[] | null = null;
let inflight: Promise<GhRepo[]> | null = null;

function fetchGh(): Promise<GhRepo[]> {
  if (cache) return Promise.resolve(cache);
  if (inflight) return inflight;
  inflight = getJSON<{ available?: boolean; repos?: GhRepo[] }>("/gh/repos")
    .then((d) => {
      cache = d && d.available ? d.repos || [] : [];
      return cache;
    })
    .catch(() => {
      cache = [];
      return cache;
    })
    .finally(() => {
      inflight = null;
    });
  return inflight;
}

// useGhRepos returns the loaded gh repos + a `loaded` flag. Loads (or reuses the
// cache) on mount.
export function useGhRepos(): { repos: GhRepo[]; loaded: boolean } {
  const [repos, setRepos] = useState<GhRepo[]>(cache || []);
  const [loaded, setLoaded] = useState<boolean>(cache != null);
  useEffect(() => {
    let alive = true;
    fetchGh().then((r) => {
      if (!alive) return;
      setRepos(r);
      setLoaded(true);
    });
    return () => {
      alive = false;
    };
  }, []);
  return { repos, loaded };
}
