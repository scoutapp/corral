import { useCallback, useEffect, useState } from "react";
import { Link } from "../router";
import { getJSON, postRaw } from "../api/client";
import type { CachedRepo, GhIssue } from "../api/types";
import { ghOwnerName, relDate } from "../lib/repos";
import { NewIssueModal, SpawnModal, NewProjectModal } from "./ReposModals";

// The Repos section content (list + per-repo issues panel + issue/spawn modals).
// Rendered inside ProjectsPage when the Repos nav item is active. Port of the
// repos half of projects-ui.js. `search` and Add-repo/refresh triggers are owned
// by the parent (ProjectsPage), which passes `search` and a `reloadKey`.
export function ReposSection({ search, reloadKey }: { search: string; reloadKey: number }) {
  const [repos, setRepos] = useState<CachedRepo[] | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [issuesFor, setIssuesFor] = useState<string | null>(null); // repo id with open panel
  const [newProject, setNewProject] = useState<{ repoId?: string } | null>(null);
  const [newIssue, setNewIssue] = useState<{ repo: CachedRepo; ownerName: string } | null>(null);
  const [spawn, setSpawn] = useState<{ repo: CachedRepo; ownerName: string; issue: GhIssue } | null>(null);

  const load = useCallback(() => {
    getJSON<{ repos?: CachedRepo[] }>("/repos")
      .then((d) => {
        setRepos(d.repos || []);
        setErr(null);
      })
      .catch((e) => setErr((e as Error).message));
  }, []);
  useEffect(() => load(), [load, reloadKey]);

  const q = search.trim().toLowerCase();
  const shown = (repos || []).filter(
    (rp) =>
      !q ||
      (rp.name || "").toLowerCase().includes(q) ||
      (rp.url || "").toLowerCase().includes(q) ||
      (rp.local_path || "").toLowerCase().includes(q),
  );

  async function refresh(rp: CachedRepo) {
    try {
      await postRaw(`/repos/${encodeURIComponent(rp.id)}/fetch`);
      load();
    } catch (e) {
      alert(`refresh failed: ${(e as Error).message}`);
    }
  }
  async function remove(rp: CachedRepo) {
    if (!window.confirm(`Remove '${rp.name}'? Spun-off projects are kept.`)) return;
    try {
      await fetch(`/repos/${encodeURIComponent(rp.id)}`, { method: "DELETE", credentials: "same-origin" });
      load();
    } catch (e) {
      alert(`remove failed: ${(e as Error).message}`);
    }
  }

  return (
    <>
      <div className="repos-list">
        {err && <p className="attention">repos error: {err}</p>}
        {repos && repos.length === 0 && <p className="muted">No repos cached yet. Add one to spin projects off it quickly.</p>}
        {repos && repos.length > 0 && shown.length === 0 && <p className="muted">No repos match “{search}”.</p>}
        {shown.map((rp) => {
          const ownerName = ghOwnerName(rp.url);
          const open = issuesFor === rp.id;
          return (
            <div className="repo-item" key={rp.id}>
              <div className="repo-row">
                <div className="repo-meta">
                  <button
                    type="button"
                    className={`repo-pin${rp.pinned ? " on" : ""}`}
                    title={rp.pinned ? "Unpin" : "Pin to top"}
                    onClick={() =>
                      postRaw(`/repos/${encodeURIComponent(rp.id)}/pin`, { pinned: !rp.pinned }).then(load)
                    }
                  >
                    {rp.pinned ? "★" : "☆"}
                  </button>
                  <Link className="repo-name" to={`/repos/${encodeURIComponent(rp.id)}`}>
                    {rp.name}
                  </Link>
                  <span className="repo-src">{rp.url || rp.local_path || ""}</span>
                  {rp.is_private && <span className="repo-badge">private</span>}
                </div>
                <div className="repo-actions">
                  <button type="button" className="btn primary" onClick={() => setNewProject({ repoId: rp.id })}>
                    Create project
                  </button>
                  <button type="button" className={`btn${open ? " on" : ""}`} disabled={!ownerName} title="Browse this repo's GitHub issues" onClick={() => setIssuesFor(open ? null : rp.id)}>
                    Issues
                  </button>
                  <button type="button" className="btn" title="Refresh cache" onClick={() => refresh(rp)}>
                    ⟳
                  </button>
                  <button type="button" className="btn" title="Remove repo" onClick={() => remove(rp)}>
                    ✕
                  </button>
                </div>
              </div>
              {open && ownerName && (
                <IssuesPanel
                  ownerName={ownerName}
                  onNewIssue={() => setNewIssue({ repo: rp, ownerName })}
                  onSpawn={(iss) => setSpawn({ repo: rp, ownerName, issue: iss })}
                />
              )}
            </div>
          );
        })}
      </div>

      {newProject && <NewProjectModal presetRepoId={newProject.repoId} onClose={() => setNewProject(null)} />}
      {newIssue && (
        <NewIssueModal repoId={newIssue.repo.id} ownerName={newIssue.ownerName} onClose={() => setNewIssue(null)} onCreated={() => setIssuesFor(newIssue.repo.id)} />
      )}
      {spawn && <SpawnModal repo={spawn.repo} ownerName={spawn.ownerName} issue={spawn.issue} onClose={() => setSpawn(null)} />}
    </>
  );
}

function IssuesPanel({ ownerName, onNewIssue, onSpawn }: { ownerName: string; onNewIssue: () => void; onSpawn: (iss: GhIssue) => void }) {
  const [issues, setIssues] = useState<GhIssue[] | null>(null);
  const [reason, setReason] = useState<string | null>(null);
  const now = Date.now();

  const load = useCallback(() => {
    setIssues(null);
    setReason(null);
    getJSON<{ available?: boolean; issues?: GhIssue[]; reason?: string }>(`/gh/issues?repo=${encodeURIComponent(ownerName)}`)
      .then((d) => {
        if (!d || !d.available) {
          setReason(d?.reason || "gh error");
          setIssues([]);
          return;
        }
        setIssues(d.issues || []);
      })
      .catch((e) => setReason((e as Error).message));
  }, [ownerName]);
  useEffect(() => load(), [load]);

  return (
    <div className="issues-panel">
      <div className="issues-head">
        <span className="muted">{ownerName}</span>
        <button type="button" className="btn" onClick={onNewIssue}>
          + New issue
        </button>
      </div>
      <div className="issues-list">
        {issues === null && <div className="ta-loading">loading issues…</div>}
        {reason && <div className="muted">couldn't load issues ({reason})</div>}
        {issues && issues.length === 0 && !reason && <div className="muted">no open issues</div>}
        {(issues || []).map((iss) => {
          const by = iss.author?.login ? `@${iss.author.login}` : "";
          const sub = [by, relDate(iss.createdAt, now)].filter(Boolean).join(" · ");
          return (
            <div className="issue-row" key={iss.number}>
              <div className="issue-meta">
                <div className="issue-titleline">
                  <span className="issue-num">#{iss.number}</span>
                  <span className="issue-title" title={iss.title}>
                    {iss.title}
                  </span>
                </div>
                {sub && <div className="issue-sub muted">{sub}</div>}
              </div>
              <button type="button" className="btn primary issue-spawn" onClick={() => onSpawn(iss)}>
                Spawn
              </button>
            </div>
          );
        })}
      </div>
    </div>
  );
}
