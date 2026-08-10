import { useEffect, useRef, useState } from "react";
import { getJSON, postJSON, postRaw, wsURL } from "../api/client";
import type { CreateProjectResponse, GhIssue, GhRepo, CachedRepo, RepoSpec } from "../api/types";
import { Modal } from "../components/Modal";
import { Typeahead } from "../components/Typeahead";
import { SSHLoadModal } from "../components/SSHLoadModal";
import { useGhRepos } from "../hooks/useGhRepos";
import { repoItems, toSpec } from "../lib/repos";

function Status({ msg }: { msg: { text: string; err: boolean } | null }) {
  if (!msg) return <span className="form-status" />;
  return <span className={`form-status${msg.err ? " error" : ""}`}>{msg.text}</span>;
}

// A single repo+branch row for multi-repo create/spawn. Loads branches when the
// value looks like a gh "owner/name".
function RepoRow({
  ghRepos,
  removable,
  onRemove,
  value,
  onChange,
}: {
  ghRepos: GhRepo[];
  removable: boolean;
  onRemove: () => void;
  value: { text: string; branch: string };
  onChange: (v: { text: string; branch: string }) => void;
}) {
  const [branches, setBranches] = useState<string[]>([]);
  const loadBranches = (val: string) => {
    const gh = ghRepos.find((g) => g.nameWithOwner === val || g.url === val);
    const ownerName = gh ? gh.nameWithOwner : /^[\w.-]+\/[\w.-]+$/.test(val) ? val : null;
    if (!ownerName) {
      setBranches([]);
      return;
    }
    getJSON<{ available?: boolean; branches?: string[] }>(`/gh/branches?repo=${encodeURIComponent(ownerName)}`)
      .then((d) => setBranches(d && d.available ? d.branches || [] : []))
      .catch(() => setBranches([]));
  };
  return (
    <div className="repo-input-row">
      <Typeahead
        className="repo-input"
        placeholder="pick a repo, or paste a URL / local path"
        items={repoItems(ghRepos)}
        value={value.text}
        onChange={(t) => {
          onChange({ ...value, text: t });
          loadBranches(t);
        }}
        onPick={loadBranches}
      />
      <Typeahead
        className="branch-input"
        placeholder="branch"
        items={branches.map((b) => ({ value: b, label: b }))}
        value={value.branch}
        onChange={(b) => onChange({ ...value, branch: b })}
      />
      <button type="button" className="btn row-rm" title="Remove" style={{ visibility: removable ? "visible" : "hidden" }} onClick={onRemove}>
        −
      </button>
    </div>
  );
}

// New-project modal: From repo(s) / Blank dir / Existing dir.
export function NewProjectModal({ presetRepoId, onClose }: { presetRepoId?: string; onClose: () => void }) {
  const { repos: ghRepos, loaded } = useGhRepos();
  const [mode, setMode] = useState<"clone" | "new" | "existing">("clone");
  const [rows, setRows] = useState<{ text: string; branch: string }[]>([{ text: "", branch: "" }]);
  const [name, setName] = useState("");
  const [path, setPath] = useState("");
  const [enforceAllowlist, setEnforceAllowlist] = useState(false); // opt-in strict; default is passthrough
  const [msg, setMsg] = useState<{ text: string; err: boolean } | null>(null);
  const [ssh, setSsh] = useState<string | null>(null); // project id awaiting SSH-key load

  // Start the newly-created project, then open it. On 409 ssh_keys_pending, load
  // keys first (SSH modal), then retry + navigate. A start failure still opens
  // the project (its page shows the state / a Start button).
  async function startAndOpen(id: string) {
    try {
      const res = await postRaw(`/p/${id}/start`);
      const b = await res.json().catch(() => ({}));
      if (res.status === 409 && b?.ssh_keys_pending) {
        setMsg({ text: "load SSH keys to start this project…", err: false });
        setSsh(id);
        return;
      }
    } catch {
      /* fall through — open the project regardless */
    }
    window.location.href = `/p/${id}/`;
  }

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setMsg({ text: "creating…", err: false });
    let body: Record<string, unknown>;
    if (mode === "new") body = { mode: "new", name: name.trim(), path: path.trim() };
    else if (mode === "existing") body = { mode: "existing", path: path.trim() };
    else {
      const specs: RepoSpec[] = [];
      if (presetRepoId) specs.push({ repoId: presetRepoId });
      for (const r of rows) {
        const s = toSpec(r.text.trim(), r.branch.trim(), ghRepos);
        if (s) specs.push(s);
      }
      if (!specs.length) {
        setMsg({ text: "add at least one repo", err: true });
        return;
      }
      body = { mode: "clone", name: name.trim(), repos: specs };
    }
    body.enforceAllowlist = enforceAllowlist;
    try {
      const res = await postJSON<CreateProjectResponse>("/projects/create", body);
      setMsg({ text: "created — starting…", err: false });
      await startAndOpen(res.id);
    } catch (err) {
      setMsg({ text: (err as Error).message, err: true });
    }
  }

  return (
    <Modal title="New project" onClose={onClose}>
      <div className="mode-tabs">
        {(["clone", "new", "existing"] as const).map((m) => (
          <button key={m} type="button" className={mode === m ? "active" : ""} onClick={() => setMode(m)}>
            {m === "clone" ? "From repo(s)" : m === "new" ? "Blank dir" : "Existing dir"}
          </button>
        ))}
      </div>
      <form className="sc-form" onSubmit={submit}>
        {mode === "clone" && (
          <>
            {!loaded && <div className="ta-loading">loading your repos…</div>}
            <div className="repo-rows">
              {rows.map((r, i) => (
                <RepoRow
                  key={i}
                  ghRepos={ghRepos}
                  removable={i > 0}
                  onRemove={() => setRows((rs) => rs.filter((_, j) => j !== i))}
                  value={r}
                  onChange={(v) => setRows((rs) => rs.map((x, j) => (j === i ? v : x)))}
                />
              ))}
            </div>
            <button type="button" className="btn add-row" onClick={() => setRows((rs) => [...rs, { text: "", branch: "" }])}>
              + add another repo
            </button>
            <label>
              Project name (optional)
              <input type="text" placeholder="defaults from the repo" autoComplete="off" value={name} onChange={(e) => setName(e.target.value)} />
            </label>
          </>
        )}
        {mode === "new" && (
          <>
            <label>
              Name <input type="text" placeholder="my-project" autoComplete="off" value={name} onChange={(e) => setName(e.target.value)} />
            </label>
            <label>
              Location (optional)
              <input type="text" placeholder="~/code  (default: managed workspaces dir)" autoComplete="off" value={path} onChange={(e) => setPath(e.target.value)} />
            </label>
            <div className="muted cfg-note">
              Parent directory for the new project. Use <code>~</code>, an absolute path, or a path relative to your home dir. Leave blank
              to keep it in sandclaude's managed workspaces.
            </div>
          </>
        )}
        {mode === "existing" && (
          <label>
            Absolute path <input type="text" placeholder="/Users/you/code/project" autoComplete="off" value={path} onChange={(e) => setPath(e.target.value)} />
          </label>
        )}
        <details className="spawn-advanced">
          <summary>Advanced</summary>
          <label className="row spawn-fw">
            <input type="checkbox" checked={enforceAllowlist} onChange={(e) => setEnforceAllowlist(e.target.checked)} />
            <span>
              More restrictive — strict allowlist (block unknown domains, no direct TCP). Default is permissive: proxy + mitm on,
              unknown domains allowed &amp; logged.
            </span>
          </label>
        </details>
        <div className="form-actions">
          <button type="submit" className="btn primary">
            {mode === "existing" ? "Register & start" : "Create & start"}
          </button>
          <Status msg={msg} />
        </div>
      </form>

      {ssh && (
        <SSHLoadModal
          projectId={ssh}
          onDone={(loaded) => {
            const id = ssh;
            setSsh(null);
            if (loaded) postRaw(`/p/${id}/start`).catch(() => {});
            window.location.href = `/p/${id}/`;
          }}
        />
      )}
    </Modal>
  );
}

// Add-repo modal: pick a gh repo (typeahead) or paste a URL / local path.
export function AddRepoModal({ onClose, onAdded }: { onClose: () => void; onAdded: () => void }) {
  const { repos: ghRepos, loaded } = useGhRepos();
  const [val, setVal] = useState("");
  const [name, setName] = useState("");
  const [priv, setPriv] = useState(false);
  const [msg, setMsg] = useState<{ text: string; err: boolean } | null>(null);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    const v = val.trim();
    if (!v) {
      setMsg({ text: "pick a repo, or paste a URL / local path", err: true });
      return;
    }
    setMsg({ text: "cloning cache mirror…", err: false });
    const isLocal = !v.includes("://") && v.charAt(0) === "/";
    const isGh = !isLocal && !v.includes("://") && v.indexOf("/") > 0;
    const body: Record<string, unknown> = { name: name.trim(), isPrivate: priv };
    if (isLocal) body.localPath = v;
    else body.url = isGh ? `https://github.com/${v}` : v;
    try {
      await postJSON("/repos", body);
      onAdded();
      onClose();
    } catch (err) {
      setMsg({ text: (err as Error).message, err: true });
    }
  }

  return (
    <Modal title="Add repository" onClose={onClose}>
      <form className="sc-form" onSubmit={submit}>
        <label>Repository</label>
        <Typeahead
          className="repo-input"
          placeholder="search your GitHub repos, or paste a URL / local path"
          items={repoItems(ghRepos)}
          value={val}
          autoFocus
          onChange={setVal}
          onPick={(v) => {
            const g = ghRepos.find((r) => r.nameWithOwner === v);
            if (g) setPriv(!!g.isPrivate);
          }}
        />
        {!loaded && <div className="ta-loading">loading your GitHub repos…</div>}
        {loaded && ghRepos.length === 0 && <div className="muted">no GitHub repos found (paste a URL or local path instead)</div>}
        <label>
          Name (optional) <input type="text" placeholder="defaults from the repo" autoComplete="off" value={name} onChange={(e) => setName(e.target.value)} />
        </label>
        <label className="row">
          <input type="checkbox" checked={priv} onChange={(e) => setPriv(e.target.checked)} /> Private (clone with your host git/gh auth)
        </label>
        <div className="form-actions">
          <button type="submit" className="btn primary">
            Add
          </button>
          <Status msg={msg} />
        </div>
      </form>
    </Modal>
  );
}

// New-issue modal: AI draft (host claude, read-only, streamed) + manual fields.
// Drafting NEVER creates the issue — you review and click Create.
export function NewIssueModal({ repoId, ownerName, onClose, onCreated }: { repoId: string; ownerName: string; onClose: () => void; onCreated: () => void }) {
  const [intent, setIntent] = useState("");
  const [title, setTitle] = useState("");
  const [body, setBody] = useState("");
  const [drafting, setDrafting] = useState(false);
  const [draftStatus, setDraftStatus] = useState("");
  const [log, setLog] = useState("");
  const [msg, setMsg] = useState<{ text: string; err: boolean } | null>(null);
  const wsRef = useRef<WebSocket | null>(null);
  const logRef = useRef<HTMLPreElement | null>(null);

  useEffect(() => {
    if (logRef.current) logRef.current.scrollTop = logRef.current.scrollHeight;
  }, [log]);
  useEffect(() => () => wsRef.current?.close(), []);

  function draft() {
    const it = intent.trim();
    if (!it) {
      setDraftStatus("describe what you want first");
      return;
    }
    wsRef.current?.close();
    setDrafting(true);
    setDraftStatus("");
    setLog("");
    const ws = new WebSocket(wsURL(`/gh/issues/draft?repoId=${encodeURIComponent(repoId)}`));
    wsRef.current = ws;
    ws.onopen = () => ws.send(JSON.stringify({ description: it }));
    ws.onmessage = (ev) => {
      let m: Record<string, unknown>;
      try {
        m = JSON.parse(ev.data);
      } catch {
        return;
      }
      if (m.type === "text") setLog((l) => l + (m.text as string));
      else if (m.type === "tool_use") setLog((l) => l + `› ${m.tool}\n`);
      else if (m.type === "error") setDraftStatus(`AI error: ${(m.text as string) || ""}`);
      else if (m.type === "draft") {
        if (m.text) setTitle(m.text as string);
        if (m.result) setBody(m.result as string);
        setDraftStatus("✓ drafted — review + edit, then Create");
      }
    };
    ws.onclose = () => {
      setDrafting(false);
      wsRef.current = null;
    };
    ws.onerror = () => setDraftStatus("draft connection failed");
  }

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    if (!title.trim()) {
      setMsg({ text: "title is required (write one or use Draft with AI)", err: true });
      return;
    }
    setMsg({ text: "creating…", err: false });
    try {
      await postJSON("/gh/issues/create", { repo: ownerName, title: title.trim(), body });
      wsRef.current?.close();
      onCreated();
      onClose();
    } catch (err) {
      setMsg({ text: (err as Error).message, err: true });
    }
  }

  return (
    <Modal title={`New issue · ${ownerName}`} onClose={onClose}>
      <form className="sc-form" onSubmit={submit}>
        <div className="ai-draft">
          <div className="ai-draft-head">
            <span className="ai-draft-title">✨ Draft with AI</span>
            <span className="ai-warn" title="Runs your host machine's claude, not the sandbox">
              host claude · not sandboxed · read-only
            </span>
          </div>
          <textarea
            rows={2}
            placeholder="describe what you want in plain words — the AI researches the repo and drafts the issue"
            value={intent}
            onChange={(e) => setIntent(e.target.value)}
          />
          <div className="form-actions">
            <button type="button" className="btn ai-draft-btn" disabled={drafting} onClick={draft}>
              {drafting ? "researching…" : "Draft with AI"}
            </button>
            <span className="ai-draft-status muted">{draftStatus}</span>
          </div>
          {log && (
            <pre className="ai-draft-log" ref={logRef}>
              {log}
            </pre>
          )}
        </div>
        <label>
          Title <input type="text" placeholder="issue title" autoComplete="off" value={title} onChange={(e) => setTitle(e.target.value)} />
        </label>
        <label>
          Body <textarea rows={6} placeholder="describe the issue (optional)" value={body} onChange={(e) => setBody(e.target.value)} />
        </label>
        <div className="form-actions">
          <button type="submit" className="btn primary">
            Create issue
          </button>
          <Status msg={msg} />
        </div>
      </form>
    </Modal>
  );
}

// Spawn modal: create a project off an issue (clone repo on a branch, write
// ISSUE.md, pre-type a prompt). Advanced = extra repos + a "more restrictive"
// (strict allowlist) opt-in; default is the permissive passthrough firewall.
// Handles the 409 ssh_keys_pending -> load -> start -> navigate flow.
export function SpawnModal({ repo, ownerName, issue, onClose }: { repo: CachedRepo; ownerName: string; issue: GhIssue; onClose: () => void }) {
  const { repos: ghRepos } = useGhRepos();
  const [enforceAllowlist, setEnforceAllowlist] = useState(false);
  const [extras, setExtras] = useState<{ text: string; branch: string }[]>([]);
  const [msg, setMsg] = useState<{ text: string; err: boolean } | null>(null);
  const [busy, setBusy] = useState(false);
  const [ssh, setSsh] = useState<{ id: string; prompt: string } | null>(null);

  function navigate(id: string, prompt: string) {
    if (prompt) postRaw(`/p/${id}/populate-prompt`, { prompt }).catch(() => {});
    window.location.href = `/p/${id}/`;
  }

  async function startSpawned(id: string, prompt: string) {
    try {
      const res = await postRaw(`/p/${id}/start`);
      const b = await res.json().catch(() => ({}));
      if (res.status === 409 && b?.ssh_keys_pending) {
        setMsg({ text: "load SSH keys to start this project…", err: false });
        setSsh({ id, prompt });
        return;
      }
      navigate(id, prompt);
    } catch {
      navigate(id, prompt);
    }
  }

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setMsg({ text: "spawning…", err: false });
    const specs: RepoSpec[] = [{ repoId: repo.id }];
    for (const r of extras) {
      const s = toSpec(r.text.trim(), r.branch.trim(), ghRepos);
      if (s) specs.push(s);
    }
    const body = {
      mode: "clone",
      repos: specs,
      name: `${repo.name}-${issue.number}`,
      issue: { number: issue.number, title: issue.title, body: issue.body || "", url: issue.url, repo: ownerName },
      enforceAllowlist,
    };
    try {
      const res = await postJSON<CreateProjectResponse>("/projects/create", body);
      startSpawned(res.id, res.issue_prompt || "");
    } catch (err) {
      setBusy(false);
      setMsg({ text: (err as Error).message, err: true });
    }
  }

  return (
    <Modal title={`Spawn project · #${issue.number}`} onClose={onClose}>
      <form className="sc-form" onSubmit={submit}>
        <div className="spawn-summary">
          <div>Spawn a project to work on:</div>
          <div className="issue-titleline">
            <span className="issue-num">#{issue.number}</span>
            <span className="issue-title" title={issue.title}>
              {issue.title}
            </span>
          </div>
          <div className="muted" style={{ fontSize: "0.78rem" }}>
            clones {ownerName} on a new branch, writes ISSUE.md, and pre-types a prompt into Claude.
          </div>
        </div>

        <details className="spawn-advanced">
          <summary>Advanced</summary>
          <label className="row spawn-fw" style={{ marginBottom: "0.5rem" }}>
            <input type="checkbox" checked={enforceAllowlist} onChange={(e) => setEnforceAllowlist(e.target.checked)} />
            <span>
              More restrictive — strict allowlist (block unknown domains, no direct TCP). Default is permissive: proxy + mitm on,
              unknown domains allowed &amp; logged.
            </span>
          </label>
          <div className="repo-rows">
            {extras.map((r, i) => (
              <RepoRow
                key={i}
                ghRepos={ghRepos}
                removable
                onRemove={() => setExtras((xs) => xs.filter((_, j) => j !== i))}
                value={r}
                onChange={(v) => setExtras((xs) => xs.map((x, j) => (j === i ? v : x)))}
              />
            ))}
          </div>
          <button type="button" className="btn add-row" onClick={() => setExtras((xs) => [...xs, { text: "", branch: "" }])}>
            + add another repo
          </button>
        </details>

        <div className="form-actions">
          <button type="submit" className="btn primary" disabled={busy}>
            Spawn project
          </button>
          <Status msg={msg} />
        </div>
      </form>

      {ssh && (
        <SSHLoadModal
          projectId={ssh.id}
          onDone={(loaded) => {
            const cur = ssh;
            setSsh(null);
            if (loaded) {
              postRaw(`/p/${cur.id}/start`).catch(() => {});
              navigate(cur.id, cur.prompt);
            } else {
              setBusy(false);
              setMsg({ text: "keys not loaded — project created but not started.", err: true });
            }
          }}
        />
      )}
    </Modal>
  );
}
