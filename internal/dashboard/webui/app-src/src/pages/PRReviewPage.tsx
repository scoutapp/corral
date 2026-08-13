import { useEffect, useState } from "react";
import { Link } from "../router";
import { getJSON, postJSON } from "../api/client";
import type { CachedRepo, PrItem } from "../api/types";
import { useBodyClass } from "../hooks/useBodyClass";
import { ChatPanel } from "../components/ChatPanel";
import {
  AnalysisStatusBanner,
  BlockCarousel,
  PRFilesForensics,
  RiskCard,
  clearFileStatsCache,
} from "./RepoPage";

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
  const [nonce, setNonce] = useState(0); // bumped after re-analyze to remount panels
  const [chatOpen, setChatOpen] = useState(false);

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
        {pr && (
          <button
            type="button"
            className={`dock-toggle${chatOpen ? " on" : ""}`}
            title="Ask Claude about this PR — a host claude chat"
            onClick={() => setChatOpen((v) => !v)}
          >
            💬 Ask Claude
          </button>
        )}
      </header>

      <div className="pr-review-page">
        {err ? (
          <p className="tab-note err">Failed to load PR #{number}: {err}</p>
        ) : !pr ? (
          <p className="tab-note">Loading PR #{number}…</p>
        ) : (
          <>
            <AnalysisStatusBanner
              repoId={repoId}
              onAnalyzed={() => {
                clearFileStatsCache(pr.id);
                setNonce((n) => n + 1);
              }}
            />
            {/* Top: Risk (left) + Files changed (right); stacks Risk-first when
                too narrow. Then the block carousel full-width below. */}
            <div className="pr-top">
              <div className="pr-top-left">
                <RiskCard key={`risk-${nonce}`} prId={pr.id} />
              </div>
              <div className="pr-top-right">
                <PRFilesForensics key={`ff-${nonce}`} prId={pr.id} />
              </div>
            </div>
            <BlockCarousel key={`bc-${nonce}`} prId={pr.id} />
          </>
        )}
      </div>

      {/* Ask-Claude drawer — reuses the projects-page ChatPanel + chrome,
          scoped to this PR (the server injects PR summary + hot-file context). */}
      {pr && chatOpen && (
        <div className="chat-panel" id="chat-panel">
          <div className="chat-panel-bar">
            <span>
              <i className="screen-dot" />
              ask claude · PR #{number}
            </span>
            <span className="chat-panel-actions">
              <button type="button" title="Close" onClick={() => setChatOpen(false)}>
                ✕
              </button>
            </span>
          </div>
          <div className="chat-panel-iframe">
            <ChatPanel wsPath={`/prs/${pr.id}/chat/ws`} />
          </div>
        </div>
      )}
    </>
  );
}
