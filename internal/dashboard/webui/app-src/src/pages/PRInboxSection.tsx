import { useEffect, useMemo, useState } from "react";
import { useRouter } from "../router";
import { getJSON } from "../api/client";
import type { InboxPr } from "../api/types";
import { relDate } from "../lib/repos";

const LAST_SEEN_KEY = "corral.prInbox.lastSeen";

// PRInboxSection is the cross-repo PR review queue: open PRs aggregated across
// every GitHub repo in your Repos list. PRs updated since your last visit sort
// above a "New" divider; the rest below. Client-side search filters the loaded
// list. Clicking a PR opens its review page.
export function PRInboxSection() {
  const { navigate } = useRouter();
  const [items, setItems] = useState<InboxPr[] | null>(null);
  const [currentUser, setCurrentUser] = useState("");
  const [scope, setScope] = useState<"all" | "mine">("all");
  const [err, setErr] = useState<string | null>(null);
  const [query, setQuery] = useState("");
  const now = Date.now();

  // The "last visit" cutoff is read once on mount (so newly-updated PRs stay
  // above the line during this visit), then advanced to now when we leave.
  const lastSeen = useMemo(() => {
    const v = localStorage.getItem(LAST_SEEN_KEY);
    return v ? Date.parse(v) : 0;
  }, []);
  useEffect(() => {
    return () => localStorage.setItem(LAST_SEEN_KEY, new Date().toISOString());
  }, []);

  useEffect(() => {
    getJSON<{ prs: InboxPr[]; currentUser?: string }>("/prs/inbox")
      .then((d) => {
        setItems(d.prs || []);
        setCurrentUser(d.currentUser || "");
      })
      .catch((e) => setErr((e as Error).message));
  }, []);

  if (err) return <p className="tab-note err">Failed to load PRs: {err}</p>;
  if (items === null) return <p className="tab-note">Loading PRs…</p>;
  if (items.length === 0)
    return (
      <p className="tab-note">
        No open PRs across your repos. Add a GitHub repo (Repos tab) to see its
        open PRs here.
      </p>
    );

  // "Mine" = PRs I authored (currentUser). Applied before the text search.
  const mineCount = currentUser ? items.filter((it) => it.pr.author === currentUser).length : 0;
  const scoped = scope === "mine" && currentUser ? items.filter((it) => it.pr.author === currentUser) : items;

  const q = query.trim().toLowerCase();
  const filtered = q
    ? scoped.filter(
        (it) =>
          String(it.pr.number).includes(q) ||
          it.pr.title.toLowerCase().includes(q) ||
          it.repoName.toLowerCase().includes(q) ||
          it.pr.author.toLowerCase().includes(q),
      )
    : scoped;

  const sorted = [...filtered].sort((a, b) =>
    (b.pr.updatedAt || "").localeCompare(a.pr.updatedAt || ""),
  );
  const isNew = (it: InboxPr) => lastSeen > 0 && Date.parse(it.pr.updatedAt || "") > lastSeen;
  const fresh = sorted.filter(isNew);
  const older = sorted.filter((it) => !isNew(it));

  const row = (it: InboxPr) => (
    <li key={`${it.repoId}-${it.pr.number}`} className="pr-row">
      <button
        type="button"
        className="pr-head"
        onClick={() => navigate(`/repos/${encodeURIComponent(it.repoId)}/prs/${it.pr.number}`)}
      >
        <span
          className="repo-color-chip"
          style={{
            borderColor: it.repoColor || "var(--con-line)",
            color: it.repoColor || "var(--con-muted)",
          }}
        >
          {it.repoName}
        </span>
        <span className="pr-num">#{it.pr.number}</span> {it.pr.title}
        <ReviewDot decision={it.pr.reviewDecision} draft={it.pr.isDraft} />
      </button>
      <div className="pr-byline">
        {it.pr.author && <span>@{it.pr.author}</span>}
        <span> · updated {relDate(it.pr.updatedAt, now)}</span>
        <span className="pr-diffstat">
          {" "}
          <span className="add">+{it.pr.additions}</span>{" "}
          <span className="del">−{it.pr.deletions}</span>
        </span>
      </div>
    </li>
  );

  return (
    <>
      <div className="pr-scope-tabs">
        <button type="button" className={`pr-scope-tab${scope === "all" ? " active" : ""}`} onClick={() => setScope("all")}>
          All <span className="pr-scope-count">{items.length}</span>
        </button>
        {currentUser && (
          <button type="button" className={`pr-scope-tab${scope === "mine" ? " active" : ""}`} onClick={() => setScope("mine")} title={`PRs authored by @${currentUser}`}>
            Mine <span className="pr-scope-count">{mineCount}</span>
          </button>
        )}
      </div>

      <div className="pr-toolbar">
        <input
          className="pr-search"
          type="search"
          placeholder="🔎 filter PRs by repo, #, title, author…"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
        />
        <span className="pr-toolbar-count">
          {filtered.length} of {scoped.length}
        </span>
      </div>

      {filtered.length === 0 && (
        <p className="tab-note">
          {scope === "mine" ? `No open PRs authored by @${currentUser}.` : "No PRs match your filter."}
        </p>
      )}
      {fresh.length > 0 && (
        <>
          <div className="inbox-divider new">▸ New (updated since last visit)</div>
          <ul className="pr-list">{fresh.map(row)}</ul>
        </>
      )}
      {older.length > 0 && (
        <>
          {fresh.length > 0 && <div className="inbox-divider">▸ Earlier</div>}
          <ul className="pr-list">{older.map(row)}</ul>
        </>
      )}
    </>
  );
}

function ReviewDot({ decision, draft }: { decision: string; draft: boolean }) {
  if (draft) return <span className="review-badge draft">draft</span>;
  if (decision === "APPROVED") return <span className="review-badge approved">✓</span>;
  if (decision === "CHANGES_REQUESTED") return <span className="review-badge changes">✗</span>;
  if (decision === "REVIEW_REQUIRED") return <span className="review-badge required">○</span>;
  return null;
}
