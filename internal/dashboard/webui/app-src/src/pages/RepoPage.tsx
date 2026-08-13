import { useCallback, useEffect, useState } from "react";
import { Link } from "../router";
import { getJSON, postJSON } from "../api/client";
import type { CachedRepo, PrBlock, PrFileStat, PrItem } from "../api/types";
import { useBodyClass } from "../hooks/useBodyClass";

// RepoPage is the repo-as-hub detail view (/repos/<id>). A repo owns three
// tabs: PR Review (grokker-derived PR intelligence), Projects (Corral sandbox
// sessions on this repo — Phase 4), and Forensics (code-health heatmap).
//
// This is the Phase-0 scaffold: routing + tab shell + store-backed reads that
// return empty lists until the analysis/fetch writers land (Phases 1–2). The
// empty states describe what each tab will do.

type Tab = "prs" | "projects" | "forensics";

const TABS: { key: Tab; label: string }[] = [
  { key: "prs", label: "PR Review" },
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
        <div className="tab-panel" style={{ display: tab === "projects" ? "block" : "none" }}>
          <ProjectsTab />
        </div>
        <div className="tab-panel" style={{ display: tab === "forensics" ? "block" : "none" }}>
          <ForensicsTab repoId={id} />
        </div>
      </div>
    </>
  );
}

function PRsTab({ repoId }: { repoId: string }) {
  const [prs, setPrs] = useState<PrItem[] | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [num, setNum] = useState("");
  const [fetching, setFetching] = useState(false);

  const load = useCallback(() => {
    getJSON<{ prs: PrItem[] }>(`/repos/${encodeURIComponent(repoId)}/prs`)
      .then((d) => setPrs(d.prs || []))
      .catch((e) => setErr((e as Error).message));
  }, [repoId]);
  useEffect(() => load(), [load]);

  const fetchPR = useCallback(() => {
    const n = parseInt(num, 10);
    if (!Number.isFinite(n) || n <= 0) {
      setErr("enter a positive PR number");
      return;
    }
    setFetching(true);
    setErr(null);
    postJSON<{ pr: PrItem }>(`/repos/${encodeURIComponent(repoId)}/prs/fetch`, { number: n })
      .then(() => {
        setNum("");
        load();
      })
      .catch((e) => setErr((e as Error).message))
      .finally(() => setFetching(false));
  }, [num, repoId, load]);

  const fetchBar = (
    <div className="tab-actions">
      <input
        className="pr-num-input"
        type="number"
        min="1"
        placeholder="PR #"
        value={num}
        disabled={fetching}
        onChange={(e) => setNum(e.target.value)}
        onKeyDown={(e) => e.key === "Enter" && fetchPR()}
      />
      <button type="button" className="btn primary" disabled={fetching} onClick={fetchPR}>
        {fetching ? "Fetching…" : "Fetch PR"}
      </button>
    </div>
  );

  const [openPR, setOpenPR] = useState<number | null>(null);

  return (
    <>
      {fetchBar}
      {err && <p className="tab-note err">Failed: {err}</p>}
      {prs === null ? (
        <p className="tab-note">Loading…</p>
      ) : prs.length === 0 ? (
        <p className="tab-note">
          No pull requests fetched yet. Enter a PR number above to pull its diff.
          Fetching splits it into hotness-ranked blocks and summarizes it.
        </p>
      ) : (
        <ul className="pr-list">
          {prs.map((p) => (
            <li key={p.id} className="pr-row">
              <button
                type="button"
                className="pr-head"
                onClick={() => setOpenPR(openPR === p.id ? null : p.id)}
              >
                <span className="pr-num">#{p.number}</span>{" "}
                {p.shortSummary || p.title || "(untitled)"}
                {p.state && <span className="pr-state">{p.state}</span>}
              </button>
              {openPR === p.id && <BlockCarousel prId={p.id} />}
            </li>
          ))}
        </ul>
      )}
    </>
  );
}

// BlockCarousel shows a PR's hotness-ranked blocks one at a time with ←/→
// navigation, mirroring the reference block-carousel wireframe: diff hunk, what
// it does, codebase context, and a hotness badge.
function BlockCarousel({ prId }: { prId: number }) {
  const [blocks, setBlocks] = useState<PrBlock[] | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [i, setI] = useState(0);

  useEffect(() => {
    getJSON<{ blocks: PrBlock[] }>(`/prs/${prId}/blocks`)
      .then((d) => {
        setBlocks(d.blocks || []);
        setI(0);
      })
      .catch((e) => setErr((e as Error).message));
  }, [prId]);

  if (err) return <p className="tab-note err">Failed to load blocks: {err}</p>;
  if (blocks === null) return <p className="tab-note">Loading blocks…</p>;
  if (blocks.length === 0)
    return <p className="tab-note">No blocks extracted for this PR.</p>;

  const b = blocks[Math.min(i, blocks.length - 1)];
  return (
    <div className="block-carousel">
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
          {b.hotnessScore != null && (
            <span className="block-hot">hotness {b.hotnessScore.toFixed(1)}</span>
          )}
        </div>
        {b.title && <h3 className="block-title">{b.title}</h3>}
        {b.diffHunk && <pre className="block-diff">{b.diffHunk}</pre>}
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
    </div>
  );
}

function ForensicsTab({ repoId }: { repoId: string }) {
  const [files, setFiles] = useState<PrFileStat[] | null>(null);
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
    postJSON<{ files: PrFileStat[] }>(`/repos/${encodeURIComponent(repoId)}/analyze`)
      .then((d) => setFiles(d.files || []))
      .catch((e) => setErr((e as Error).message))
      .finally(() => setAnalyzing(false));
  }, [repoId]);

  const analyzeBtn = (
    <button type="button" className="btn primary" disabled={analyzing} onClick={analyze}>
      {analyzing ? "Analyzing…" : files && files.length ? "Re-analyze" : "Analyze repo"}
    </button>
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

function ProjectsTab() {
  return (
    <p className="tab-note">
      Corral sandbox sessions started from this repo will appear here (filtered
      from your projects by their git remote).
    </p>
  );
}
