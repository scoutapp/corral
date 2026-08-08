import type { GhRepo, RepoSpec } from "../api/types";
import type { TAItem } from "../components/Typeahead";

// Derive "owner/name" from a github URL; null if it isn't a github repo URL.
export function ghOwnerName(url?: string): string | null {
  if (!url) return null;
  const m = String(url).match(/github\.com[:/]+([^/]+)\/([^/]+?)(?:\.git)?\/?$/);
  return m ? `${m[1]}/${m[2]}` : null;
}

// Short "3 days ago"-style relative date from an ISO timestamp. Takes `now` so
// callers can pass Date.now() (this module stays pure).
export function relDate(iso: string | undefined, now: number): string {
  if (!iso) return "";
  const then = new Date(iso).getTime();
  if (isNaN(then)) return "";
  const s = Math.max(0, (now - then) / 1000);
  const units: [string, number][] = [
    ["y", 31536000],
    ["mo", 2592000],
    ["d", 86400],
    ["h", 3600],
    ["m", 60],
  ];
  for (const [label, secs] of units) {
    const n = Math.floor(s / secs);
    if (n >= 1) return `${n}${label} ago`;
  }
  return "just now";
}

export function repoItems(ghRepos: GhRepo[]): TAItem[] {
  return ghRepos.map((g) => ({ value: g.nameWithOwner, label: g.nameWithOwner, hint: g.isPrivate ? "private" : "" }));
}

// Turn a typed/picked repo value + branch into a create-project repo spec.
export function toSpec(text: string, branch: string, ghRepos: GhRepo[]): RepoSpec | null {
  if (!text) return null;
  const spec: RepoSpec = { branch: branch || undefined };
  const gh = ghRepos.find((g) => g.nameWithOwner === text || g.url === text);
  if (gh) {
    spec.url = gh.url;
    return spec;
  }
  if (!text.includes("://") && text.charAt(0) === "/") spec.localPath = text;
  else spec.url = text;
  return spec;
}
