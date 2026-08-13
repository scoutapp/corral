import { useCallback, useEffect, useState } from "react";
import { Link } from "../router";
import { getJSON, postJSON } from "../api/client";
import type { CachedRepo, PrFileStat, PrItem } from "../api/types";
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

  return (
    <>
      {fetchBar}
      {err && <p className="tab-note err">Failed: {err}</p>}
      {prs === null ? (
        <p className="tab-note">Loading…</p>
      ) : prs.length === 0 ? (
        <p className="tab-note">
          No pull requests fetched yet. Enter a PR number above to pull its diff.
          Hotness-ranked blocks and an AI summary land in a later phase.
        </p>
      ) : (
        <ul className="pr-list">
          {prs.map((p) => (
            <li key={p.id}>
              <span className="pr-num">#{p.number}</span>{" "}
              {p.title || p.shortSummary || "(untitled)"}
              {p.state && <span className="pr-state">{p.state}</span>}
            </li>
          ))}
        </ul>
      )}
    </>
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
