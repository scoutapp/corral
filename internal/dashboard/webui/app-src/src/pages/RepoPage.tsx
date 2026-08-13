import { useCallback, useEffect, useRef, useState } from "react";
import { Link, useRouter } from "../router";
import { getJSON, postJSON } from "../api/client";
import type {
  AnalysisStatus,
  CachedRepo,
  FileForensic,
  GhIssue,
  LinkSuggestion,
  OpenPr,
  PrBlock,
  PrFileStat,
  PrItem,
  PrLink,
  PrRisk,
  RepoProject,
} from "../api/types";
import { useBodyClass } from "../hooks/useBodyClass";
import { loadEditor, type DiffHandle, type DiffEditorView } from "../lib/editor";
import { splitUnifiedHunk } from "../lib/diffHunk";
import { relDate, ghOwnerName } from "../lib/repos";

// RepoPage is the repo-as-hub detail view (/repos/<id>). A repo owns three
// tabs: PR Review (grokker-derived PR intelligence), Projects (Corral sandbox
// sessions on this repo — Phase 4), and Forensics (code-health heatmap).
//
// This is the Phase-0 scaffold: routing + tab shell + store-backed reads that
// return empty lists until the analysis/fetch writers land (Phases 1–2). The
// empty states describe what each tab will do.

type Tab = "prs" | "issues" | "projects" | "forensics";

const TABS: { key: Tab; label: string }[] = [
  { key: "prs", label: "PR Review" },
  { key: "issues", label: "Issues" },
  { key: "projects", label: "Projects" },
  { key: "forensics", label: "Forensics" },
];

export function RepoPage({ id }: { id: string }) {
  useBodyClass("console");
  const [tab, setTab] = useState<Tab>("prs");
  const [repo, setRepo] = useState<CachedRepo | null>(null);

  // Find this repo in the cached list for its header (name/url). The repos list
  // is small; a dedicated GET /repos/<id> can come later if needed.
  useEffect(() => {
    getJSON<{ repos: CachedRepo[] }>("/repos")
      .then((d) => setRepo((d.repos || []).find((r) => r.id === id) || null))
      .catch(() => setRepo(null));
  }, [id]);

  return (
    <>
      <header className="console-header">
        <div className="brand">
          <Link to="/" className="back">
            ← All repos
          </Link>
          <span className="brand-name">{repo ? repo.name : id}</span>
          {repo?.url && (
            <a className="brand-sub repo-url" href={repo.url} target="_blank" rel="noreferrer">
              {repo.url.replace(/^https?:\/\//, "")}
            </a>
          )}
          <RepoAnalyzedChip repoId={id} />
        </div>
      </header>

      <div className="repo-hub tab-area">
        <div className="tabs">
          {TABS.map((t) => (
            <button
              key={t.key}
              className={`tab-btn${tab === t.key ? " active" : ""}`}
              onClick={() => setTab(t.key)}
            >
              {t.label}
            </button>
          ))}
        </div>

        <div className="tab-panel" style={{ display: tab === "prs" ? "block" : "none" }}>
          <PRsTab repoId={id} />
        </div>
        <div className="tab-panel" style={{ display: tab === "issues" ? "block" : "none" }}>
          <IssuesTab repoUrl={repo?.url} />
        </div>
        <div className="tab-panel" style={{ display: tab === "projects" ? "block" : "none" }}>
          <ProjectsTab repoId={id} />
        </div>
        <div className="tab-panel" style={{ display: tab === "forensics" ? "block" : "none" }}>
          <ForensicsTab repoId={id} />
        </div>
      </div>
    </>
  );
}

// RepoAnalyzedChip shows a persistent header chip when the repo hasn't been
// analyzed (or has new commits since), visible across all repo tabs.
function RepoAnalyzedChip({ repoId }: { repoId: string }) {
  const [st, setSt] = useState<AnalysisStatus | null>(null);
  useEffect(() => {
    getJSON<AnalysisStatus>(`/repos/${encodeURIComponent(repoId)}/analysis-status`)
      .then(setSt)
      .catch(() => {});
  }, [repoId]);
  if (!st) return null;
  if (!st.analyzed) return <span className="repo-chip warn">⚠ not analyzed</span>;
  if (!st.upToDate) return <span className="repo-chip warn">⚠ analysis out of date</span>;
  return null;
}

// ReviewBadge shows a PR's approval state (from gh's reviewDecision), or a
// draft pill. Draft takes precedence since a draft can't be reviewed.
function ReviewBadge({ decision, draft }: { decision: string; draft: boolean }) {
  if (draft) return <span className="review-badge draft">draft</span>;
  switch (decision) {
    case "APPROVED":
      return <span className="review-badge approved">✓ approved</span>;
    case "CHANGES_REQUESTED":
      return <span className="review-badge changes">✗ changes</span>;
    case "REVIEW_REQUIRED":
      return <span className="review-badge required">○ review</span>;
    default:
      return <span className="review-badge none">—</span>;
  }
}

function PRsTab({ repoId }: { repoId: string }) {
  // Live open PRs from GitHub (gh pr list), plus the PRs already fetched into
  // the DB (to show a "✓ viewed" marker). Clicking a PR navigates to its
  // dedicated review page, which Views it (fetch + blocks) on arrival.
  const { navigate } = useRouter();
  const [open, setOpen] = useState<OpenPr[] | null>(null);
  const [openUnavailable, setOpenUnavailable] = useState<string | null>(null);
  const [fetched, setFetched] = useState<PrItem[]>([]);
  const [err, setErr] = useState<string | null>(null);
  const [num, setNum] = useState("");

  const loadFetched = useCallback(
    () =>
      getJSON<{ prs: PrItem[] }>(`/repos/${encodeURIComponent(repoId)}/prs`)
        .then((d) => setFetched(d.prs || []))
        .catch(() => {}),
    [repoId],
  );
  const loadOpen = useCallback(() => {
    getJSON<{ available: boolean; prs?: OpenPr[]; reason?: string }>(
      `/repos/${encodeURIComponent(repoId)}/prs/open`,
    )
      .then((d) => {
        if (d.available) {
          setOpen(d.prs || []);
          setOpenUnavailable(null);
        } else {
          setOpen([]);
          setOpenUnavailable(d.reason || "unavailable");
        }
      })
      .catch((e) => setOpenUnavailable((e as Error).message));
  }, [repoId]);

  useEffect(() => {
    loadOpen();
    loadFetched();
  }, [loadOpen, loadFetched]);

  // Map PR number -> already-fetched DB record (for the "viewed" marker).
  const fetchedByNum = new Map(fetched.map((p) => [p.number, p]));

  // Overview controls (all client-side over the loaded list — no server calls).
  const [query, setQuery] = useState("");
  const [sort, setSort] = useState<"updated" | "created" | "number">("updated");
  const [hideDrafts, setHideDrafts] = useState(false);
  const now = Date.now();

  const visible = (open || [])
    .filter((p) => !hideDrafts || !p.isDraft)
    .filter((p) => {
      const q = query.trim().toLowerCase();
      if (!q) return true;
      return (
        String(p.number).includes(q) ||
        p.title.toLowerCase().includes(q) ||
        p.author.toLowerCase().includes(q) ||
        p.labels.some((l) => l.toLowerCase().includes(q))
      );
    })
    .sort((a, b) => {
      if (sort === "number") return b.number - a.number;
      const key = sort === "updated" ? "updatedAt" : "createdAt";
      return (b[key] || "").localeCompare(a[key] || "");
    });

  const openManual = () => {
    const n = parseInt(num, 10);
    if (!Number.isFinite(n) || n <= 0) {
      setErr("enter a positive PR number");
      return;
    }
    navigate(`/repos/${encodeURIComponent(repoId)}/prs/${n}`);
  };

  return (
    <>
      {/* Surface analyze/staleness on the default landing view too, not just
          the Forensics tab — so users know rankings need analysis. */}
      <AnalysisStatusBanner repoId={repoId} />
      {err && <p className="tab-note err">Failed: {err}</p>}

      {open === null ? (
        <p className="tab-note">Loading open PRs…</p>
      ) : openUnavailable ? (
        <p className="tab-note">
          Couldn't list open PRs ({openUnavailable}). Open a PR by number below.
        </p>
      ) : open.length === 0 ? (
        <p className="tab-note">No open pull requests on this repo.</p>
      ) : (
        <>
          <div className="pr-toolbar">
            <input
              className="pr-search"
              type="search"
              placeholder="🔎 filter by #, title, author, label…"
              value={query}
              onChange={(e) => setQuery(e.target.value)}
            />
            <select value={sort} onChange={(e) => setSort(e.target.value as typeof sort)}>
              <option value="updated">Recently updated</option>
              <option value="created">Recently created</option>
              <option value="number">PR number</option>
            </select>
            <label className="pr-toolbar-check">
              <input type="checkbox" checked={hideDrafts} onChange={(e) => setHideDrafts(e.target.checked)} />
              hide drafts
            </label>
            <span className="pr-toolbar-count">
              {visible.length} of {open.length}
            </span>
          </div>

          {visible.length === 0 ? (
            <p className="tab-note">No PRs match “{query}”.</p>
          ) : (
            <ul className="pr-list rich">
              {visible.map((p) => {
                const rec = fetchedByNum.get(p.number);
                return (
                  <li key={p.number} className="pr-card">
                    <Link
                      className="pr-card-link"
                      to={`/repos/${encodeURIComponent(repoId)}/prs/${p.number}`}
                    >
                      <div className="pr-card-top">
                        <ReviewBadge decision={p.reviewDecision} draft={p.isDraft} />
                        <span className="pr-card-title">
                          <span className="pr-num">#{p.number}</span>{" "}
                          {rec?.shortSummary || p.title || "(untitled)"}
                        </span>
                        {rec && <span className="pr-analyzed" title="Viewed">✓</span>}
                      </div>
                      <div className="pr-card-meta">
                        {p.author && <span className="pr-meta-author">@{p.author}</span>}
                        <span className="pr-diffstat">
                          <span className="add">+{p.additions.toLocaleString()}</span>{" "}
                          <span className="del">−{p.deletions.toLocaleString()}</span>
                        </span>
                        <span className="pr-meta-time">updated {relDate(p.updatedAt, now)}</span>
                        {p.labels.slice(0, 4).map((l) => (
                          <span key={l} className="pr-label">{l}</span>
                        ))}
                      </div>
                    </Link>
                  </li>
                );
              })}
            </ul>
          )}
        </>
      )}

      <div className="pr-manual">
        <span className="pr-manual-h">Open a specific PR (e.g. closed/old):</span>
        <input
          className="pr-num-input"
          type="number"
          min="1"
          placeholder="PR #"
          value={num}
          onChange={(e) => setNum(e.target.value)}
          onKeyDown={(e) => e.key === "Enter" && openManual()}
        />
        <button type="button" className="btn" onClick={openManual}>
          Open
        </button>
      </div>
    </>
  );
}

// BlockCarousel shows a PR's hotness-ranked blocks one at a time with ←/→
// navigation, mirroring the reference block-carousel wireframe: diff hunk, what
// it does, codebase context, and a hotness badge.
// A block's explanation matches this placeholder when it hasn't been AI-enriched
// (see prreview placeholderAnalysis). Used to show the "Add AI analysis" prompt.
const PLACEHOLDER_EXPLANATION = "This block modifies the file. Claude analysis is unavailable.";

interface BlocksStatus {
  repoAnalyzed: boolean;
  stale: boolean;
}

export function BlockCarousel({ prId }: { prId: number }) {
  const [blocks, setBlocks] = useState<PrBlock[] | null>(null);
  const [status, setStatus] = useState<BlocksStatus | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [i, setI] = useState(0);
  const [enriching, setEnriching] = useState(false);
  const [reranking, setReranking] = useState(false);

  const load = useCallback(() => {
    getJSON<{ blocks: PrBlock[]; status?: BlocksStatus }>(`/prs/${prId}/blocks`)
      .then((d) => {
        setBlocks(d.blocks || []);
        setStatus(d.status || null);
        setI(0);
      })
      .catch((e) => setErr((e as Error).message));
  }, [prId]);
  useEffect(() => load(), [load]);

  const enrich = () => {
    setEnriching(true);
    setErr(null);
    postJSON<{ blocks: PrBlock[]; status?: BlocksStatus }>(`/prs/${prId}/enrich`)
      .then((d) => {
        setBlocks(d.blocks || []);
        if (d.status) setStatus(d.status);
      })
      .catch((e) => setErr((e as Error).message))
      .finally(() => setEnriching(false));
  };

  const rerank = () => {
    setReranking(true);
    setErr(null);
    postJSON<{ blocks: PrBlock[]; status?: BlocksStatus }>(`/prs/${prId}/rerank`)
      .then((d) => {
        setBlocks(d.blocks || []);
        if (d.status) setStatus(d.status);
      })
      .catch((e) => setErr((e as Error).message))
      .finally(() => setReranking(false));
  };

  if (err) return <p className="tab-note err">Failed to load blocks: {err}</p>;
  if (blocks === null) return <p className="tab-note">Loading blocks…</p>;
  if (blocks.length === 0)
    return <p className="tab-note">No blocks extracted for this PR.</p>;

  // "Enriched" if any block carries a non-placeholder explanation.
  const enriched = blocks.some(
    (bl) => bl.explanation && bl.explanation !== PLACEHOLDER_EXPLANATION,
  );

  const b = blocks[Math.min(i, blocks.length - 1)];
  return (
    <div className="block-carousel">
      {status?.stale && (
        <div className="rerank-bar">
          <span>
            ⚠ Block hotness was ranked before the repo's last analysis — the
            ordering may be out of date.
          </span>
          <button type="button" className="btn" disabled={reranking} onClick={rerank}>
            {reranking ? "Re-ranking…" : "Re-rank blocks"}
          </button>
        </div>
      )}
      {status && !status.repoAnalyzed && (
        <div className="rerank-bar">
          <span>
            ⚠ This repo isn't analyzed yet — blocks aren't hotness-ranked. Run
            "Analyze repo" (Forensics tab) to rank by churn &amp; callgraph.
          </span>
        </div>
      )}
      <div className="enrich-bar">
        {enriched ? (
          <span className="enrich-note">✓ AI analysis added</span>
        ) : (
          <span className="enrich-note">
            Blocks ranked by hotness (churn × callgraph). Add Claude's per-block
            explanations, edge cases, and a summary:
          </span>
        )}
        <button type="button" className="btn" disabled={enriching} onClick={enrich}>
          {enriching ? "Analyzing…" : enriched ? "Re-run AI" : "+ Add AI analysis"}
        </button>
      </div>
      <div className="block-nav">
        <button type="button" disabled={i <= 0} onClick={() => setI(i - 1)}>
          ◀
        </button>
        <span className="block-pos">
          Block {i + 1} of {blocks.length}
        </span>
        <button
          type="button"
          disabled={i >= blocks.length - 1}
          onClick={() => setI(i + 1)}
        >
          ▶
        </button>
        <span className="block-dots">
          {blocks.map((_, j) => (
            <span key={j} className={`dot${j === i ? " on" : ""}`} />
          ))}
        </span>
      </div>

      <div className="block-card">
        <div className="block-meta">
          <span className="block-prio">🔥 Priority {b.priority}</span>
          <span className="block-loc">
            {b.filePath}:{b.lineStart}–{b.lineEnd}
          </span>
          {b.isTest && <span className="block-badge">test</span>}
          {b.diffHunk && <BlockDiffStat hunk={b.diffHunk} />}
          {b.hotnessScore != null && (
            <span className="block-hot">hotness {b.hotnessScore.toFixed(1)}</span>
          )}
        </div>
        {b.title && <h3 className="block-title">{b.title}</h3>}
        {b.diffHunk && (
          <BlockDiff
            prId={prId}
            hunk={b.diffHunk}
            filePath={b.filePath}
            lineStart={b.lineStart}
          />
        )}
        {b.explanation && (
          <>
            <h4 className="block-h">What this does</h4>
            <p>{b.explanation}</p>
          </>
        )}
        {b.codebaseContext && (
          <>
            <h4 className="block-h">Codebase context</h4>
            <p>{b.codebaseContext}</p>
          </>
        )}
      </div>

      <LinkedPRs prId={prId} />
    </div>
  );
}

// BlockDiff renders a block's unified diff hunk as a real syntax-highlighted,
// side-by-side diff using the committed CodeMirror bundle (same one the Diff
// tab uses) — restoring highlighting that the plain <pre> lost. The hunk is
// split into before/after text; if the editor bundle fails to load, we fall
// back to the raw hunk in a <pre>.
function BlockDiff({
  prId,
  hunk,
  filePath,
  lineStart,
}: {
  prId: number;
  hunk: string;
  filePath: string;
  lineStart: number;
}) {
  const hostRef = useRef<HTMLDivElement | null>(null); // outer wrapper (hover math)
  const bodyRef = useRef<HTMLDivElement | null>(null); // CM mount point (.diff-body)
  const viewRef = useRef<DiffEditorView | null>(null);
  const [failed, setFailed] = useState(false);
  // The CM line (1-based, in the modified doc) the '+' hovers over, and its
  // screen y; and the line a comment box is open for.
  const [hoverLine, setHoverLine] = useState<{ cmLine: number; top: number } | null>(null);
  const [commentLine, setCommentLine] = useState<number | null>(null);

  useEffect(() => {
    let handle: DiffHandle | null = null;
    let cancelled = false;
    let scrollCleanup: (() => void) | null = null;
    const { original, modified } = splitUnifiedHunk(hunk);
    loadEditor()
      .then((editor) => {
        if (cancelled || !bodyRef.current) return;
        bodyRef.current.innerHTML = "";
        handle = editor.createDiff({
          parent: bodyRef.current,
          original,
          modified,
          filename: filePath,
        });
        viewRef.current = handle.view || null;
        // While the diff scrolls, the pinned '+' would point at the wrong row
        // (no mousemove fires) — hide it until the next hover.
        const scroller = handle.view?.dom.querySelector(".cm-scroller");
        if (scroller) {
          const onScroll = () => setHoverLine(null);
          scroller.addEventListener("scroll", onScroll, { passive: true });
          scrollCleanup = () => scroller.removeEventListener("scroll", onScroll);
        }
      })
      .catch(() => !cancelled && setFailed(true));
    return () => {
      cancelled = true;
      scrollCleanup?.();
      handle?.destroy();
      viewRef.current = null;
    };
  }, [hunk, filePath]);

  // Track which line the cursor is over so we can pin a '+' comment button to
  // that row (GitHub-style). The button lives INSIDE the scroll host, so moving
  // toward it never leaves the hover target. We update on move but only clear
  // when the cursor leaves the whole wrapper.
  const onMove = (e: React.MouseEvent) => {
    const view = viewRef.current;
    const host = hostRef.current;
    if (!view || !host) return;
    // Snap to the row at the cursor's y, using the host's left edge so hovering
    // over the gutter/button still resolves to the right line.
    const hostRect = host.getBoundingClientRect();
    const pos = view.posAtCoords({ x: hostRect.left + 12, y: e.clientY });
    if (pos == null) return;
    const cmLine = view.state.doc.lineAt(pos).number; // 1-based
    const coords = view.coordsAtPos(pos);
    if (!coords) return;
    // Viewport-relative row top → offset within the host. The button is absolute
    // inside .block-diff-host (which doesn't itself scroll — CM scrolls inside),
    // so this tracks the row's on-screen position without adding scrollTop.
    const rowTop = coords.top - hostRect.top;
    setHoverLine((cur) =>
      cur?.cmLine === cmLine && Math.abs(cur.top - rowTop) < 1 ? cur : { cmLine, top: rowTop },
    );
  };

  if (failed) return <pre className="diff-pre">{hunk}</pre>;
  // Real new-file line = block start + offset into the modified (after) doc.
  const realLine = (cmLine: number) => lineStart + (cmLine - 1);

  return (
    <div className="block-diff-wrap">
      <div
        className="block-diff-host diff-view"
        ref={hostRef}
        onMouseMove={onMove}
        onMouseLeave={() => setHoverLine(null)}
      >
        <div className="diff-body" ref={bodyRef} />
        {/* "+" pinned to the hovered row, inside the scroll host so it stays
            clickable as the cursor moves onto it. */}
        {hoverLine && commentLine == null && (
          <button
            type="button"
            className="line-comment-add"
            style={{ top: `${hoverLine.top}px` }}
            title={`Comment on line ${realLine(hoverLine.cmLine)}`}
            onClick={() => setCommentLine(hoverLine.cmLine)}
          >
            +
          </button>
        )}
      </div>
      {commentLine != null && (
        <LineCommentBox
          prId={prId}
          path={filePath}
          line={realLine(commentLine)}
          onClose={() => setCommentLine(null)}
        />
      )}
    </div>
  );
}

// LineCommentBox posts a review comment anchored to a diff line (side=RIGHT).
function LineCommentBox({
  prId,
  path,
  line,
  onClose,
}: {
  prId: number;
  path: string;
  line: number;
  onClose: () => void;
}) {
  const [body, setBody] = useState("");
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState<string | null>(null);

  const submit = () => {
    if (!body.trim()) return;
    setBusy(true);
    setMsg(null);
    postJSON(`/prs/${prId}/line-comment`, { path, line, side: "RIGHT", body })
      .then(() => {
        setMsg("✓ posted");
        setTimeout(onClose, 700);
      })
      .catch((e) => setMsg((e as Error).message))
      .finally(() => setBusy(false));
  };

  return (
    <div className="line-comment-box">
      <div className="line-comment-head">
        Comment on {path}:{line}
        <button type="button" className="line-comment-x" onClick={onClose}>
          ✕
        </button>
      </div>
      <textarea
        className="line-comment-body"
        placeholder="Leave a review comment on this line…"
        value={body}
        autoFocus
        onChange={(e) => setBody(e.target.value)}
      />
      <div className="line-comment-actions">
        <button type="button" className="btn primary" disabled={busy || !body.trim()} onClick={submit}>
          {busy ? "Posting…" : "Comment"}
        </button>
        {msg && <span className={`line-comment-msg${msg.startsWith("✓") ? "" : " err"}`}>{msg}</span>}
      </div>
    </div>
  );
}

// FileForensicsRow shows the git/callgraph forensics for the block's file:
// fix ratio, author diversity (sole-contributor flag), age + staleness, edit
// velocity, and callgraph reference count. Fetches the PR's file-stats once and
// caches them on the PR id so switching blocks doesn't refetch.
const fileStatsCache = new Map<number, Promise<FileForensic[]>>();
function getFileStats(prId: number): Promise<FileForensic[]> {
  let p = fileStatsCache.get(prId);
  if (!p) {
    p = getJSON<{ files: FileForensic[] }>(`/prs/${prId}/file-stats`).then((d) => d.files || []);
    fileStatsCache.set(prId, p);
  }
  return p;
}

// clearFileStatsCache drops a PR's cached file-stats so the next fetch re-reads
// (e.g. after a repo Re-analyze refreshes the underlying forensics).
export function clearFileStatsCache(prId: number) {
  fileStatsCache.delete(prId);
}

// FileForensicsChips renders the git/callgraph stat chips for one file. Files
// with no commit history (repo not analyzed, or a brand-new file) show a muted
// "not analyzed" chip instead of misleading zeros.
// BlockDiffStat computes +/- from a block's diff hunk (client-side).
function BlockDiffStat({ hunk }: { hunk: string }) {
  let add = 0;
  let del = 0;
  for (const line of hunk.split("\n")) {
    if (line.startsWith("+") && !line.startsWith("+++")) add++;
    else if (line.startsWith("-") && !line.startsWith("---")) del++;
  }
  if (add === 0 && del === 0) return null;
  return (
    <span className="block-diffstat">
      <span className="add">+{add}</span> <span className="del">−{del}</span>
    </span>
  );
}

// DiffStatChip renders a +add / −del count (per file or PR total).
function DiffStatChip({ add, del }: { add: number; del: number }) {
  if (add === 0 && del === 0) return null;
  return (
    <span className="ff-chip diffstat" title="lines added / removed in this PR">
      <span className="add">+{add.toLocaleString()}</span>{" "}
      <span className="del">−{del.toLocaleString()}</span>
    </span>
  );
}

function FileForensicsChips({ stat }: { stat: FileForensic }) {
  const hasHistory = stat.totalCommits > 0 || stat.daysOld != null;
  if (!hasHistory) {
    // No git history: either the PR adds this file (new), or the repo simply
    // hasn't been analyzed. Distinguish so a new file isn't mislabeled.
    const label = stat.newFile
      ? "✨ new file"
      : stat.repoAnalyzed
        ? "no history"
        : "not analyzed";
    const title = stat.newFile
      ? "added by this PR — no prior history on the base branch"
      : stat.repoAnalyzed
        ? "no commit history for this file on the analyzed branch"
        : "run 'Analyze repo' to compute file forensics";
    return (
      <div className="file-forensics">
        <DiffStatChip add={stat.additions} del={stat.deletions} />
        <span className="ff-chip cool" title={title}>
          {label}
        </span>
        {stat.refCount > 0 && <span className="ff-chip">🔗 {stat.refCount} refs</span>}
      </div>
    );
  }
  const sole = stat.authorCount === 1;
  const stale = stat.daysSinceEdit != null && stat.daysSinceEdit > 180;
  return (
    <div className="file-forensics">
      <DiffStatChip add={stat.additions} del={stat.deletions} />
      <span className="ff-chip" title="fix commits / total commits">
        🔧 {stat.fixCommits}/{stat.totalCommits} fixes ({stat.fixPct}%)
      </span>
      <span className={`ff-chip${sole ? " warn" : ""}`} title="distinct commit authors">
        👤 {stat.authorCount} author{stat.authorCount === 1 ? "" : "s"}
        {sole ? " (sole)" : ""}
      </span>
      {stat.refCount > 0 && (
        <span className="ff-chip" title="other files that call into this file (callgraph)">
          🔗 {stat.refCount} refs
        </span>
      )}
      {stat.velocityPerWeek > 0 && (
        <span className="ff-chip" title="commits per week over the file's life">
          ⚡ {stat.velocityPerWeek}/wk
        </span>
      )}
      {stat.daysOld != null && (
        <span className="ff-chip" title="age since first commit">
          📅 {stat.daysOld}d old
        </span>
      )}
      {stat.daysSinceEdit != null && (
        <span className={`ff-chip${stale ? " cool" : ""}`} title="days since last edit">
          🕐 edited {stat.daysSinceEdit}d ago{stale ? " (stale)" : ""}
        </span>
      )}
    </div>
  );
}

// AnalysisStatusBanner surfaces repo-analysis freshness: an "Analyze repo"
// prompt when never analyzed, or a "N new commits since analysis" notice (with
// the commit list) + Re-analyze when the mirror has moved on. Nothing when
// up to date. Re-analyze runs the repo forensics + callgraph (POST
// /repos/<id>/analyze) and calls onAnalyzed so callers can refresh. Reused by
// the Forensics tab and the PR page.
export function AnalysisStatusBanner({
  repoId,
  onAnalyzed,
}: {
  repoId: string;
  onAnalyzed?: () => void;
}) {
  const [st, setSt] = useState<AnalysisStatus | null>(null);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  const load = useCallback(() => {
    getJSON<AnalysisStatus>(`/repos/${encodeURIComponent(repoId)}/analysis-status`)
      .then(setSt)
      .catch(() => {});
  }, [repoId]);
  useEffect(() => load(), [load]);

  const analyze = () => {
    setBusy(true);
    setErr(null);
    postJSON(`/repos/${encodeURIComponent(repoId)}/analyze`)
      .then(() => {
        load();
        onAnalyzed?.();
      })
      .catch((e) => setErr((e as Error).message))
      .finally(() => setBusy(false));
  };

  if (!st) return null;
  if (st.analyzed && st.upToDate) return null; // nothing to nag about

  if (!st.analyzed) {
    return (
      <div className="analysis-banner">
        <span>
          This repo hasn't been analyzed. Run forensics + the callgraph to power
          hotness ranking and file stats.
        </span>
        <button type="button" className="btn primary" disabled={busy} onClick={analyze}>
          {busy ? "Analyzing…" : "Analyze repo"}
        </button>
        {err && <span className="tab-note err">{err}</span>}
      </div>
    );
  }

  const n = st.newCommits?.length || 0;
  return (
    <div className="analysis-banner warn">
      <div className="analysis-banner-head">
        <span>
          ⚠ {n > 0 ? `${n}${n >= 25 ? "+" : ""} new commit${n === 1 ? "" : "s"}` : "New commits"}{" "}
          since analysis (at {st.analyzedSha}). Ranking &amp; file stats may be out of date.
        </span>
        <button type="button" className="btn" disabled={busy} onClick={analyze}>
          {busy ? "Analyzing…" : "Re-analyze"}
        </button>
      </div>
      {st.newCommits && st.newCommits.length > 0 && (
        <ul className="analysis-commits">
          {st.newCommits.map((c) => (
            <li key={c.sha}>
              <span className="ac-sha">{c.sha}</span> {c.subject}
            </li>
          ))}
        </ul>
      )}
      {err && <span className="tab-note err">{err}</span>}
    </div>
  );
}

// How many files the "Files changed" panel shows before "view more".
const FILES_COLLAPSED = 5;

// PRFilesForensics is the page-top panel listing the files the PR touches with
// their forensics, HOTTEST FIRST (server-ordered by max block hotness). Collapsed
// to the top few by default with a "view more" toggle. Shows skeleton rows while
// /file-stats resolves so the page renders immediately. Used by PRReviewPage.
export function PRFilesForensics({ prId }: { prId: number }) {
  const [files, setFiles] = useState<FileForensic[] | null>(null);
  const [expanded, setExpanded] = useState(false);
  useEffect(() => {
    let live = true;
    getFileStats(prId)
      .then((f) => live && setFiles(f))
      .catch(() => live && setFiles([]));
    return () => {
      live = false;
    };
  }, [prId]);

  const total = files?.length ?? 0;
  const shown = files && !expanded ? files.slice(0, FILES_COLLAPSED) : files;
  const hidden = total - (shown?.length ?? 0);
  const totalAdd = (files || []).reduce((s, f) => s + f.additions, 0);
  const totalDel = (files || []).reduce((s, f) => s + f.deletions, 0);

  return (
    <div className="pr-files">
      <h3 className="pr-files-h">
        Files changed{files ? ` (${total})` : ""}
        {files && (totalAdd > 0 || totalDel > 0) && (
          <span className="pr-files-total">
            <span className="add">+{totalAdd.toLocaleString()}</span>{" "}
            <span className="del">−{totalDel.toLocaleString()}</span>
          </span>
        )}
      </h3>
      {files === null ? (
        <div className="pr-files-list">
          {[0, 1, 2].map((i) => (
            <div className="pr-file-row skeleton" key={i}>
              <span className="sk sk-name" />
              <span className="sk sk-chip" />
              <span className="sk sk-chip" />
              <span className="sk sk-chip" />
            </div>
          ))}
        </div>
      ) : files.length === 0 ? (
        <p className="tab-note">No file forensics for this PR.</p>
      ) : (
        <>
          <div className="pr-files-list">
            {shown!.map((f) => (
              <div className="pr-file-row" key={f.filePath}>
                <span className="pr-file-name" title={f.filePath}>
                  {f.filePath}
                </span>
                <FileForensicsChips stat={f} />
              </div>
            ))}
          </div>
          {total > FILES_COLLAPSED && (
            <button
              type="button"
              className="pr-files-more"
              onClick={() => setExpanded((v) => !v)}
            >
              {expanded ? "Show less" : `View ${hidden} more file${hidden === 1 ? "" : "s"}`}
            </button>
          )}
        </>
      )}
    </div>
  );
}

// RiskCard shows (and can compute) the PR-level risk verdict. GET /prs/<id>/risk
// loads a stored verdict; "Assess risk" runs POST /prs/<id>/analyze via claude.
export function RiskCard({ prId }: { prId: number }) {
  const [risk, setRisk] = useState<PrRisk | null>(null);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    getJSON<{ risk: PrRisk | null }>(`/prs/${prId}/risk`)
      .then((d) => setRisk(d.risk))
      .catch(() => {});
  }, [prId]);

  const assess = () => {
    setBusy(true);
    setErr(null);
    postJSON<{ risk: PrRisk }>(`/prs/${prId}/analyze`)
      .then((d) => setRisk(d.risk))
      .catch((e) => setErr((e as Error).message))
      .finally(() => setBusy(false));
  };

  return (
    <div className="risk-card">
      <div className="risk-head">
        <span className="risk-title">Risk</span>
        {risk && (
          <span className={`risk-pill ${risk.overallRisk}`}>{risk.overallRisk}</span>
        )}
        <button type="button" className="btn" disabled={busy} onClick={assess}>
          {busy ? "Assessing…" : risk ? "Re-assess" : "Assess risk"}
        </button>
      </div>
      {err && <p className="tab-note err">Failed: {err}</p>}
      {risk && (
        <div className="risk-body">
          <p className="risk-summary">{risk.riskSummary}</p>
          {risk.meat && <p><strong>Change:</strong> {risk.meat}</p>}
          {risk.bugImpact && <p><strong>If a bug slips in:</strong> {risk.bugImpact}</p>}
          {risk.fixHistory && <p><strong>Fix history:</strong> {risk.fixHistory}</p>}
          {risk.fileHealth?.length > 0 && (
            <ul className="risk-files">
              {risk.fileHealth.map((f, k) => (
                <li key={k}>
                  <span className={`risk-dot ${f.risk}`} /> {f.file} — {f.insight}
                </li>
              ))}
            </ul>
          )}
        </div>
      )}
    </div>
  );
}

interface AnalyzeResp {
  files: PrFileStat[];
  cgNodes?: number;
  cgEdges?: number;
  callgraphOk?: boolean;
}

function ForensicsTab({ repoId }: { repoId: string }) {
  const [files, setFiles] = useState<PrFileStat[] | null>(null);
  const [cg, setCg] = useState<{ nodes: number; edges: number; ok: boolean } | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [analyzing, setAnalyzing] = useState(false);

  const load = useCallback(() => {
    getJSON<{ files: PrFileStat[] }>(`/repos/${encodeURIComponent(repoId)}/forensics`)
      .then((d) => setFiles(d.files || []))
      .catch((e) => setErr((e as Error).message));
  }, [repoId]);
  useEffect(() => load(), [load]);

  const analyze = useCallback(() => {
    setAnalyzing(true);
    setErr(null);
    postJSON<AnalyzeResp>(`/repos/${encodeURIComponent(repoId)}/analyze`)
      .then((d) => {
        setFiles(d.files || []);
        setCg({ nodes: d.cgNodes || 0, edges: d.cgEdges || 0, ok: !!d.callgraphOk });
      })
      .catch((e) => setErr((e as Error).message))
      .finally(() => setAnalyzing(false));
  }, [repoId]);

  const analyzeBtn = (
    <>
      <button type="button" className="btn primary" disabled={analyzing} onClick={analyze}>
        {analyzing ? "Analyzing…" : files && files.length ? "Re-analyze" : "Analyze repo"}
      </button>
      {cg &&
        (cg.ok ? (
          <span className="cg-stat">
            Callgraph: {cg.nodes.toLocaleString()} nodes · {cg.edges.toLocaleString()} edges
          </span>
        ) : (
          <span className="cg-stat">Callgraph unavailable (churn-only hotness)</span>
        ))}
    </>
  );

  if (err) {
    return (
      <>
        <div className="tab-actions">{analyzeBtn}</div>
        <p className="tab-note err">Failed: {err}</p>
      </>
    );
  }
  if (files === null) return <p className="tab-note">Loading…</p>;
  if (files.length === 0) {
    return (
      <>
        <div className="tab-actions">{analyzeBtn}</div>
        <p className="tab-note">
          Not analyzed yet. Forensics ranks files by churn (commits per day) and
          fix-commit count to surface the repo's hottest, most bug-prone files.
        </p>
      </>
    );
  }
  return (
    <>
      <AnalysisStatusBanner repoId={repoId} onAnalyzed={load} />
      <div className="tab-actions">{analyzeBtn}</div>
      <table className="forensics-table">
        <thead>
          <tr>
            <th>File</th>
            <th>Churn</th>
            <th>Fix / total commits</th>
          </tr>
        </thead>
        <tbody>
          {files.map((f) => (
            <tr key={f.id}>
              <td>{f.filePath}</td>
              <td>{f.churnScore != null ? f.churnScore.toFixed(2) : "—"}</td>
              <td>
                {f.fixCommits} / {f.totalCommits}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </>
  );
}

// IssuesTab lists the repo's open GitHub issues (GET /gh/issues?repo=owner/name,
// the same endpoint the create-from-issue flow uses), with in-memory search.
// Each links to GitHub; "Start project" spawns a project seeded from the issue.
function IssuesTab({ repoUrl }: { repoUrl?: string }) {
  const owner = ghOwnerName(repoUrl);
  const [issues, setIssues] = useState<GhIssue[] | null>(null);
  const [reason, setReason] = useState<string | null>(null);
  const [query, setQuery] = useState("");
  const now = Date.now();

  useEffect(() => {
    if (!owner) {
      setReason("not a GitHub remote");
      setIssues([]);
      return;
    }
    getJSON<{ available: boolean; issues?: GhIssue[]; reason?: string }>(
      `/gh/issues?repo=${encodeURIComponent(owner)}`,
    )
      .then((d) => {
        setIssues(d.issues || []);
        setReason(d.available ? null : d.reason || "unavailable");
      })
      .catch((e) => setReason((e as Error).message));
  }, [owner]);

  if (issues === null) return <p className="tab-note">Loading issues…</p>;
  if (reason && issues.length === 0)
    return <p className="tab-note">Couldn't list issues ({reason}).</p>;
  if (issues.length === 0) return <p className="tab-note">No open issues.</p>;

  const q = query.trim().toLowerCase();
  const visible = q
    ? issues.filter(
        (i) =>
          String(i.number).includes(q) ||
          i.title.toLowerCase().includes(q) ||
          (i.author?.login || "").toLowerCase().includes(q),
      )
    : issues;

  return (
    <>
      <div className="pr-toolbar">
        <input
          className="pr-search"
          type="search"
          placeholder="🔎 filter issues by #, title, author…"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
        />
        <span className="pr-toolbar-count">
          {visible.length} of {issues.length}
        </span>
      </div>
      <ul className="pr-list">
        {visible.map((i) => (
          <li key={i.number} className="pr-row">
            <div className="pr-head-row">
              <a
                className="pr-head"
                href={i.url}
                target="_blank"
                rel="noreferrer"
                title="Open on GitHub"
              >
                <span className="pr-num">#{i.number}</span> {i.title}
              </a>
            </div>
            <div className="pr-byline">
              {i.author?.login && <span>@{i.author.login}</span>}
              {i.createdAt && <span> · opened {relDate(i.createdAt, now)}</span>}
            </div>
          </li>
        ))}
      </ul>
    </>
  );
}

function ProjectsTab({ repoId }: { repoId: string }) {
  const [projects, setProjects] = useState<RepoProject[] | null>(null);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    getJSON<{ projects: RepoProject[] }>(`/repos/${encodeURIComponent(repoId)}/projects`)
      .then((d) => setProjects(d.projects || []))
      .catch((e) => setErr((e as Error).message));
  }, [repoId]);

  if (err) return <p className="tab-note err">Failed to load projects: {err}</p>;
  if (projects === null) return <p className="tab-note">Loading…</p>;
  if (projects.length === 0) {
    return (
      <p className="tab-note">
        No Corral sandbox sessions on this repo yet. Projects whose git remote
        matches this repo appear here — start one from the Repos list.
      </p>
    );
  }
  return (
    <ul className="pr-list">
      {projects.map((p) => (
        <li key={p.id}>
          <Link to={`/p/${encodeURIComponent(p.id)}`}>{p.name}</Link>
          <span className="proj-ws">{p.workspace}</span>
        </li>
      ))}
    </ul>
  );
}

// LinkedPRs shows a PR's cross-PR relationships and lets the user link/unlink,
// with file-overlap suggestions. Rendered inside the block carousel (PR level).
function LinkedPRs({ prId }: { prId: number }) {
  const [links, setLinks] = useState<PrLink[]>([]);
  const [suggestions, setSuggestions] = useState<LinkSuggestion[]>([]);
  const [err, setErr] = useState<string | null>(null);

  const load = useCallback(() => {
    getJSON<{ links: PrLink[] }>(`/prs/${prId}/links`)
      .then((d) => setLinks(d.links || []))
      .catch((e) => setErr((e as Error).message));
    getJSON<{ suggestions: LinkSuggestion[] }>(`/prs/${prId}/links/suggest`)
      .then((d) => setSuggestions(d.suggestions || []))
      .catch(() => {});
  }, [prId]);
  useEffect(() => load(), [load]);

  const add = (linkedPrId: number, relationship: string) => {
    postJSON(`/prs/${prId}/links`, { linkedPrId, relationship })
      .then(() => load())
      .catch((e) => setErr((e as Error).message));
  };
  // Link removal is a DELETE; the shared client only exposes GET/POST, so use
  // fetch directly (same-origin cookie carries auth).
  const del = (linkId: number) => {
    fetch(`/prs/${prId}/links/${linkId}`, { method: "DELETE", credentials: "same-origin" })
      .then(() => load())
      .catch(() => {});
  };

  return (
    <div className="linked-prs">
      <h4 className="block-h">Linked PRs</h4>
      {err && <p className="tab-note err">{err}</p>}
      {links.length === 0 && <p className="tab-note">No linked PRs.</p>}
      <ul className="link-list">
        {links.map((l) => (
          <li key={l.id}>
            <span className="link-rel">{l.relationship}</span>
            <span className="link-tgt">
              #{l.linkedNumber} {l.linkedTitle || l.linkedSummary || ""}
            </span>
            <button type="button" className="link-x" title="Remove link" onClick={() => del(l.id)}>
              ✕
            </button>
          </li>
        ))}
      </ul>
      {suggestions.length > 0 && (
        <div className="link-suggest">
          <span className="link-suggest-h">Suggested (shared files):</span>
          {suggestions.map((s) => (
            <span key={s.prId} className="link-suggest-item">
              #{s.number} ({s.overlap})
              <button type="button" className="btn tiny" onClick={() => add(s.prId, "related")}>
                + link
              </button>
            </span>
          ))}
        </div>
      )}
    </div>
  );
}
