import { useCallback, useEffect, useRef, useState } from "react";
import { getJSON } from "../api/client";
import type { GitChange, GitFileResponse, GitRefsResponse, GitReposResponse, GitStatusResponse } from "../api/types";
import { loadEditor, type DiffHandle } from "../lib/editor";

// Diff tab: what has Claude changed? Lists working-tree changes (or base..target
// when both refs are chosen) and shows a syntax-highlighted CodeMirror diff for a
// selected file. Port of diff.js, including the multi-repo picker (#115) and the
// default trunk..<current-branch> range for issue-spawned projects (#116).

function api(projectId: string, p: string) {
  return `/p/${projectId}${p}`;
}

function statusLabel(xy: string): string {
  const t = xy.trim();
  if (t === "??") return "new";
  if (t.includes("M")) return "modified";
  if (t.includes("A")) return "added";
  if (t.includes("D")) return "deleted";
  if (t.includes("R")) return "renamed";
  return t || "changed";
}

export function DiffTab({ projectId }: { projectId: string }) {
  const [repos, setRepos] = useState<GitReposResponse | null>(null);
  const [repo, setRepo] = useState("");
  const [refs, setRefs] = useState<string[]>([]);
  const [base, setBase] = useState("");
  const [target, setTarget] = useState("");
  const [changes, setChanges] = useState<GitChange[] | null>(null);
  const [notRepo, setNotRepo] = useState(false);
  const [activePath, setActivePath] = useState<string | null>(null);

  const bodyRef = useRef<HTMLDivElement | null>(null);
  const diffRef = useRef<DiffHandle | null>(null);
  const refsAutoTried = useRef(false);

  const refsActive = base !== "" && target !== "";
  const refQuery = useCallback(() => {
    let q = repo ? `&repo=${encodeURIComponent(repo)}` : "";
    if (refsActive) q += `&base=${encodeURIComponent(base)}&target=${encodeURIComponent(target)}`;
    return q;
  }, [repo, base, target, refsActive]);

  // Load repos once; auto-select a single subdir repo (#115).
  useEffect(() => {
    getJSON<GitReposResponse>(api(projectId, "/git/repos"))
      .then((data) => {
        setRepos(data);
        const list = data.repos || [];
        if (list.length <= 1) {
          if (list.length === 1 && list[0].path && !data.rootIsRepo) setRepo(list[0].path);
        } else {
          setRepo(data.rootIsRepo ? "" : list[0].path);
        }
      })
      .catch(() => {});
  }, [projectId]);

  // Load refs for the current repo; default trunk..current for feature branches.
  useEffect(() => {
    const q = repo ? `?repo=${encodeURIComponent(repo)}` : "";
    getJSON<GitRefsResponse>(api(projectId, `/git/refs${q}`))
      .then((data) => {
        if (!data.repo) {
          setRefs([]);
          return;
        }
        const b = data.branches || [];
        setRefs(b.concat(data.tags || []));
        if (!refsAutoTried.current && !refsActive && b.length > 0) {
          refsAutoTried.current = true;
          const cur = data.current || "";
          const trunk = ["main", "master", "trunk"].filter((t) => b.includes(t))[0];
          if (cur && trunk && cur !== trunk) {
            setBase(trunk);
            setTarget(cur);
          }
        }
      })
      .catch(() => {});
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [projectId, repo]);

  // Load the changed-file list whenever repo/refs change.
  useEffect(() => {
    setChanges(null);
    setNotRepo(false);
    getJSON<GitStatusResponse>(api(projectId, `/git/status?_=1${refQuery()}`))
      .then((data) => {
        if (!data.repo) {
          setNotRepo(true);
          return;
        }
        setChanges(data.changes || []);
      })
      .catch(() => setChanges([]));
  }, [projectId, refQuery]);

  const loadDiff = useCallback(
    async (path: string) => {
      setActivePath(path);
      try {
        const data = await getJSON<GitFileResponse>(
          api(projectId, `/git/file?path=${encodeURIComponent(path)}${refQuery()}`),
        );
        if ((data.original || "") === (data.modified || "")) {
          diffRef.current?.destroy();
          diffRef.current = null;
          if (bodyRef.current) bodyRef.current.innerHTML = '<span class="muted">no differences in this file</span>';
          return;
        }
        const editor = await loadEditor();
        diffRef.current?.destroy();
        if (bodyRef.current) bodyRef.current.innerHTML = "";
        diffRef.current = editor.createDiff({
          parent: bodyRef.current!,
          original: data.original || "",
          modified: data.modified || "",
          filename: data.filename || path,
        });
      } catch (e) {
        if (bodyRef.current)
          bodyRef.current.innerHTML = `<span class="attention">diff error: ${(e as Error).message}</span>`;
      }
    },
    [projectId, refQuery],
  );

  // Auto-select the first changed file when the list (re)loads.
  useEffect(() => {
    if (changes && changes.length > 0) loadDiff(changes[0].path);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [changes]);

  const hasMultiRepo = (repos?.repos?.length || 0) > 1;

  return (
    <div className="diff-root">
      <div className="diff-side">
        {hasMultiRepo && (
          <div className="diff-repo">
            <select
              className="diff-ref"
              title="Repository"
              value={repo}
              onChange={(e) => {
                setRepo(e.target.value);
                setBase("");
                setTarget("");
              }}
            >
              {(repos?.repos || []).map((rp) => (
                <option key={rp.path} value={rp.path}>
                  {rp.name}
                </option>
              ))}
            </select>
          </div>
        )}
        <div className="diff-refs">
          <select className="diff-ref" title="Base ref" value={base} onChange={(e) => setBase(e.target.value)}>
            <option value="">— working tree —</option>
            {refs.map((r) => (
              <option key={r} value={r}>
                {r}
              </option>
            ))}
          </select>
          <span className="diff-refs-arrow">→</span>
          <select className="diff-ref" title="Target ref" value={target} onChange={(e) => setTarget(e.target.value)}>
            <option value="">— working tree —</option>
            {refs.map((r) => (
              <option key={r} value={r}>
                {r}
              </option>
            ))}
          </select>
          {refsActive && (
            <button
              type="button"
              title="Back to working-tree changes"
              onClick={() => {
                setBase("");
                setTarget("");
              }}
            >
              ✕
            </button>
          )}
        </div>
        <div className="diff-list">
          {notRepo && <p className="muted">not a git repository</p>}
          {!notRepo && changes === null && <p className="muted">loading…</p>}
          {!notRepo && changes && changes.length === 0 && (
            <p className="muted">
              {refsActive ? `no differences between ${base} and ${target}` : "working tree clean — no changes"}
            </p>
          )}
          {(changes || []).map((c) => (
            <div
              key={c.path}
              className={`diff-file${activePath === c.path ? " active" : ""}`}
              onClick={() => loadDiff(c.path)}
            >
              <span className="d-status">{statusLabel(c.status)}</span>
              <span className="d-path">{c.path}</span>
              {(c.added || c.removed) && (
                <span className="d-stat">
                  <span className="d-add">+{c.added}</span> <span className="d-del">-{c.removed}</span>
                </span>
              )}
            </div>
          ))}
        </div>
      </div>
      <div className="diff-view">
        <div className="diff-body" ref={bodyRef}>
          <span className="muted">select a changed file</span>
        </div>
      </div>
    </div>
  );
}
