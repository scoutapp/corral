import { useCallback, useEffect, useState } from "react";
import { getJSON, postJSON } from "../api/client";

// AgentContextStaleBanner — a slim, dismissible banner shown wherever a repo's
// AGENTS.md agent context is in play (its Settings page, a sandbox derived from
// it, and its PR reviews) when that context has gone stale (older than the
// global staleness window, default ~3 months). Mirrors UpdateBanner: a one-click
// action (here, "Regenerate" — kicks the same AI draft as Settings) plus a
// dismiss that persists.
//
// Backed by GET /api/repos/<id>/agent-context, which returns { stale, ageDays,
// updatedAt, thresholdDays } alongside the content, so every surface shares one
// staleness read. Renders nothing unless the context is present AND stale.

type ContextStatus = {
  content: string;
  stale: boolean;
  ageDays: number;
  updatedAt: string;
  thresholdDays: number;
};

// Dismissed per repo + updatedAt: regenerating the context bumps updatedAt, which
// clears the dismissal so a freshly-stale context can nudge again later.
function dismissKey(repoId: string, updatedAt: string) {
  return `corral.agentctx.staleDismissed.${repoId}.${updatedAt}`;
}
function lsGet(k: string): string {
  try {
    return localStorage.getItem(k) || "";
  } catch {
    return "";
  }
}
function lsSet(k: string, v: string) {
  try {
    localStorage.setItem(k, v);
  } catch {
    /* ignore */
  }
}

// A rough "3 months" / "5 weeks" phrasing from a day count.
function humanAge(days: number): string {
  if (days >= 60) return `${Math.round(days / 30)} months`;
  if (days >= 14) return `${Math.round(days / 7)} weeks`;
  return `${days} days`;
}

export function AgentContextStaleBanner({ repoId }: { repoId?: string }) {
  const [status, setStatus] = useState<ContextStatus | null>(null);
  const [dismissed, setDismissed] = useState(false);
  const [regenMsg, setRegenMsg] = useState<string | null>(null);

  const load = useCallback(() => {
    if (!repoId) return;
    getJSON<ContextStatus>(`/api/repos/${encodeURIComponent(repoId)}/agent-context`)
      .then((d) => {
        setStatus(d);
        setDismissed(lsGet(dismissKey(repoId, d.updatedAt)) === "1");
      })
      .catch(() => setStatus(null));
  }, [repoId]);
  useEffect(() => load(), [load]);

  if (!repoId || !status || !status.stale || dismissed) return null;

  const dismiss = () => {
    lsSet(dismissKey(repoId, status.updatedAt), "1");
    setDismissed(true);
  };

  const regenerate = async () => {
    try {
      await postJSON(`/api/repos/${encodeURIComponent(repoId)}/generate-agents-md`, {});
      setRegenMsg("Regenerating AGENTS.md — watch the Work tab (⌘K); it updates here when done.");
    } catch (e) {
      setRegenMsg(`Couldn't start regeneration: ${(e as Error).message}`);
    }
  };

  if (regenMsg) {
    return (
      <div className="update-banner" role="status">
        <span className="update-banner-text">{regenMsg}</span>
        <span className="update-banner-actions">
          <button className="update-banner-dismiss" title="Dismiss" onClick={() => setRegenMsg(null)}>
            ✕
          </button>
        </span>
      </div>
    );
  }

  return (
    <div className="update-banner update-banner-warn" role="status">
      <span className="update-banner-text">
        This repo's <strong>AGENTS.md</strong> context is <strong>{humanAge(status.ageDays)}</strong> old{" "}
        <span className="muted">(stale after {status.thresholdDays} days)</span> — the codebase has likely drifted
        from it.
      </span>
      <span className="update-banner-actions">
        <button className="update-banner-btn" onClick={regenerate} title="Explore the repo and redraft it with AI">
          Regenerate
        </button>
        <button className="update-banner-dismiss" title="Dismiss" onClick={dismiss}>
          ✕
        </button>
      </span>
    </div>
  );
}
