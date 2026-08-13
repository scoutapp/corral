import { useEffect, useState } from "react";
import { Link } from "../router";
import { getJSON, postJSON } from "../api/client";
import type { CachedRepo, PrItem } from "../api/types";
import { useBodyClass } from "../hooks/useBodyClass";
import { BlockCarousel } from "./RepoPage";

// PRReviewPage is the dedicated full-page PR review at /repos/<id>/prs/<number>
// (the reference's PRView, not an inline popout). Navigating here Views the PR
// (fetch diff + extract hotness-ranked blocks, no AI) if it isn't already, then
// renders the full-width block carousel with its risk card, file forensics,
// chat, and linked-PRs panels. AI enrichment is an explicit action in the
// carousel.
export function PRReviewPage({ repoId, number }: { repoId: string; number: number }) {
  useBodyClass("console");
  const [repo, setRepo] = useState<CachedRepo | null>(null);
  const [pr, setPr] = useState<PrItem | null>(null);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    getJSON<{ repos: CachedRepo[] }>("/repos")
      .then((d) => setRepo((d.repos || []).find((r) => r.id === repoId) || null))
      .catch(() => {});
  }, [repoId]);

  // Resolve the PR to its stored record. It may already be viewed (in /prs); if
  // not, View it (idempotent fetch upserts + extracts blocks without AI).
  useEffect(() => {
    let live = true;
    getJSON<{ prs: PrItem[] }>(`/repos/${encodeURIComponent(repoId)}/prs`)
      .then((d) => {
        const existing = (d.prs || []).find((p) => p.number === number);
        if (existing) {
          if (live) setPr(existing);
          return;
        }
        return postJSON<{ pr: PrItem }>(`/repos/${encodeURIComponent(repoId)}/prs/fetch`, {
          number,
        }).then((r) => live && setPr(r.pr));
      })
      .catch((e) => live && setErr((e as Error).message));
    return () => {
      live = false;
    };
  }, [repoId, number]);

  return (
    <>
      <header className="console-header">
        <div className="brand">
          <Link to={`/repos/${encodeURIComponent(repoId)}`} className="back">
            ← {repo ? repo.name : "repo"}
          </Link>
          <span className="brand-name">
            #{number} {pr?.shortSummary || pr?.title || ""}
          </span>
          {pr?.state && <span className="pr-state">{pr.state}</span>}
          {pr?.githubUrl && (
            <a className="brand-sub" href={pr.githubUrl} target="_blank" rel="noreferrer">
              view on GitHub ↗
            </a>
          )}
        </div>
      </header>

      <div className="pr-review-page">
        {err ? (
          <p className="tab-note err">Failed to load PR #{number}: {err}</p>
        ) : !pr ? (
          <p className="tab-note">Loading PR #{number}…</p>
        ) : (
          <BlockCarousel prId={pr.id} />
        )}
      </div>
    </>
  );
}
