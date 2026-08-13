import { useCallback, useEffect, useRef, useState } from "react";
import { Link } from "../router";
import { getJSON, postJSON, wsURL } from "../api/client";
import type {
  CachedRepo,
  LinkSuggestion,
  PrBlock,
  PrFileStat,
  PrItem,
  PrLink,
  PrRisk,
  RepoProject,
} from "../api/types";
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
          <ProjectsTab repoId={id} />
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
      <RiskCard prId={prId} />
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

      <BlockChat prId={prId} blockId={b.id} blockLabel={b.title || b.filePath} />
      <LinkedPRs prId={prId} />
    </div>
  );
}

// RiskCard shows (and can compute) the PR-level risk verdict. GET /prs/<id>/risk
// loads a stored verdict; "Assess risk" runs POST /prs/<id>/analyze via claude.
function RiskCard({ prId }: { prId: number }) {
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

// BlockChat is a lightweight, block-scoped chat over /prs/<id>/chat/ws. It
// speaks the same server frame protocol as the project Ask-Claude panel
// (text/error/turn_end) but is scoped to the current block via ?block=. The
// server injects PR/block context into the first turn. Collapsed by default.
function BlockChat({ prId, blockId, blockLabel }: { prId: number; blockId: number; blockLabel: string }) {
  const [open, setOpen] = useState(false);
  const [msgs, setMsgs] = useState<{ role: "user" | "assistant" | "meta"; text: string }[]>([]);
  const [input, setInput] = useState("");
  const [ready, setReady] = useState(false);
  const [busy, setBusy] = useState(false);
  const wsRef = useRef<WebSocket | null>(null);
  const curIdx = useRef<number | null>(null);

  // A fresh socket per (block, open) so context is re-injected for the block
  // being viewed. Closing the drawer tears the socket down.
  useEffect(() => {
    if (!open) return;
    setMsgs([]);
    const ws = new WebSocket(wsURL(`/prs/${prId}/chat/ws?block=${blockId}`));
    wsRef.current = ws;
    ws.onopen = () => setReady(true);
    ws.onclose = () => {
      setReady(false);
      setBusy(false);
    };
    ws.onmessage = (ev) => {
      let m: { type?: string; text?: string };
      try {
        m = JSON.parse(ev.data);
      } catch {
        return;
      }
      if (m.type === "text" && m.text) {
        setMsgs((prev) => {
          const next = [...prev];
          if (curIdx.current == null) {
            curIdx.current = next.length;
            next.push({ role: "assistant", text: m.text || "" });
          } else {
            next[curIdx.current] = {
              role: "assistant",
              text: (next[curIdx.current]?.text || "") + m.text,
            };
          }
          return next;
        });
      } else if (m.type === "error") {
        setMsgs((prev) => [...prev, { role: "meta", text: m.text || "error" }]);
      } else if (m.type === "turn_end") {
        setBusy(false);
        curIdx.current = null;
      }
    };
    return () => ws.close();
  }, [open, prId, blockId]);

  const send = () => {
    const text = input.trim();
    if (!text || !ready || busy) return;
    wsRef.current?.send(JSON.stringify({ prompt: text }));
    setMsgs((prev) => [...prev, { role: "user", text }]);
    setInput("");
    setBusy(true);
  };

  if (!open) {
    return (
      <button type="button" className="btn block-chat-toggle" onClick={() => setOpen(true)}>
        💬 Ask about this block
      </button>
    );
  }

  return (
    <div className="block-chat">
      <div className="block-chat-head">
        <span>Ask Claude · {blockLabel}</span>
        <button type="button" className="block-chat-x" onClick={() => setOpen(false)}>
          ✕
        </button>
      </div>
      <div className="block-chat-log">
        {msgs.map((m, k) => (
          <div key={k} className={`chat-msg ${m.role}`}>
            {m.text}
          </div>
        ))}
        {!ready && <div className="chat-msg meta">connecting…</div>}
      </div>
      <div className="block-chat-input">
        <input
          value={input}
          placeholder="Ask about this block…"
          disabled={!ready || busy}
          onChange={(e) => setInput(e.target.value)}
          onKeyDown={(e) => e.key === "Enter" && send()}
        />
        <button type="button" className="btn primary" disabled={!ready || busy} onClick={send}>
          {busy ? "…" : "Send"}
        </button>
      </div>
      <p className="block-chat-note">Runs your host Claude (not sandboxed).</p>
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
