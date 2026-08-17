import { useCallback, useEffect, useState } from "react";
import { getJSON, postJSON, delJSON } from "../api/client";

// RepoSkillsSettings — a repo's Skills & context: skills and a CLAUDE.md-style
// agent context that get injected into a sandbox checkout of this repo at
// project-create. Backed by /api/skills (repo-scoped) and
// /api/repos/<id>/agent-context.

type RepoSkill = { id: number; name: string; content: string };

export function RepoSkillsSettings({ repoId }: { repoId: string }) {
  const [skills, setSkills] = useState<RepoSkill[]>([]);
  const [editing, setEditing] = useState<number | "new" | null>(null);
  const [draftName, setDraftName] = useState("");
  const [draftContent, setDraftContent] = useState("");
  const [msg, setMsg] = useState<{ text: string; err: boolean } | null>(null);

  const [context, setContext] = useState("");
  const [contextDirty, setContextDirty] = useState(false);

  const load = useCallback(() => {
    getJSON<{ skills: RepoSkill[] }>(`/api/skills?repo=${encodeURIComponent(repoId)}`)
      .then((d) => setSkills(d.skills || []))
      .catch((e) => setMsg({ text: (e as Error).message, err: true }));
    getJSON<{ content: string }>(`/api/repos/${encodeURIComponent(repoId)}/agent-context`)
      .then((d) => {
        setContext(d.content || "");
        setContextDirty(false);
      })
      .catch(() => {});
  }, [repoId]);
  useEffect(() => load(), [load]);

  function startNew() {
    setEditing("new");
    setDraftName("");
    setDraftContent("---\nname: my-skill\ndescription: When to use this skill.\n---\n\n");
  }
  function startEdit(sk: RepoSkill) {
    setEditing(sk.id);
    setDraftName(sk.name);
    setDraftContent(sk.content);
  }

  async function saveSkill() {
    if (!draftName.trim() || !draftContent.trim()) {
      setMsg({ text: "A name and SKILL.md content are required.", err: true });
      return;
    }
    try {
      if (editing === "new") {
        await postJSON("/api/skills", { repo: repoId, name: draftName.trim(), content: draftContent });
      } else if (typeof editing === "number") {
        await putJSON(`/api/skills/${editing}`, { name: draftName.trim(), content: draftContent });
      }
      setEditing(null);
      setMsg({ text: `Saved "${draftName.trim()}".`, err: false });
      load();
    } catch (e) {
      setMsg({ text: `Couldn't save: ${(e as Error).message}`, err: true });
    }
  }

  async function removeSkill(sk: RepoSkill) {
    if (!confirm(`Delete the "${sk.name}" skill?`)) return;
    try {
      await delJSON(`/api/skills/${sk.id}`);
      load();
    } catch (e) {
      setMsg({ text: `Couldn't delete: ${(e as Error).message}`, err: true });
    }
  }

  async function saveContext() {
    try {
      await putJSON(`/api/repos/${encodeURIComponent(repoId)}/agent-context`, { content: context });
      setContextDirty(false);
      setMsg({ text: "Context saved.", err: false });
    } catch (e) {
      setMsg({ text: `Couldn't save context: ${(e as Error).message}`, err: true });
    }
  }

  return (
    <div className="reposkills">
      <hr className="repo-settings-sep" />
      <h3 className="repo-settings-h">Skills &amp; context</h3>
      <p className="tab-note">
        Skills and an <code>AGENT.md</code> that Corral drops into a sandbox started from this repo — so
        Claude lands with the right capabilities and knowledge for this codebase.
      </p>

      {msg && <div className={`auto-msg${msg.err ? " err" : ""}`}>{msg.text}</div>}

      {/* Skills */}
      <div className="reposkills-head">
        <h4 className="reposkills-h4">Skills</h4>
        {editing === null && (
          <button type="button" className="auto-btn" onClick={startNew}>
            + New skill
          </button>
        )}
      </div>

      {editing !== null ? (
        <div className="reposkills-editor">
          <input
            className="auto-input"
            placeholder="skill name (letters, digits, - or _)"
            value={draftName}
            onChange={(e) => setDraftName(e.target.value)}
          />
          <textarea
            className="auto-input reposkills-md"
            placeholder="SKILL.md — YAML frontmatter (name, description) then the instructions."
            value={draftContent}
            onChange={(e) => setDraftContent(e.target.value)}
            rows={10}
          />
          <div className="reposkills-editor-actions">
            <button type="button" className="auto-btn" onClick={saveSkill}>
              Save skill
            </button>
            <button type="button" className="auto-btn link" onClick={() => setEditing(null)}>
              Cancel
            </button>
          </div>
        </div>
      ) : skills.length === 0 ? (
        <p className="auto-empty">No skills yet. Add one to give this repo's sandbox extra capabilities.</p>
      ) : (
        <ul className="reposkills-list">
          {skills.map((sk) => (
            <li key={sk.id} className="reposkills-item">
              <span className="reposkills-name">{sk.name}</span>
              <div className="reposkills-item-actions">
                <button type="button" className="auto-btn link" onClick={() => startEdit(sk)}>
                  edit
                </button>
                <button type="button" className="reposkills-x" title="Delete" onClick={() => removeSkill(sk)}>
                  ×
                </button>
              </div>
            </li>
          ))}
        </ul>
      )}

      {/* Agent context (CLAUDE.md) */}
      <h4 className="reposkills-h4" style={{ marginTop: "1.1rem" }}>
        AGENT.md context
      </h4>
      <p className="tab-note" style={{ marginTop: 0 }}>
        Added to the sandbox's <code>CLAUDE.md</code> (below the repo's own, if any). Use it for standing
        instructions — conventions, gotchas, where things live.
      </p>
      <textarea
        className="auto-input reposkills-md"
        placeholder="# Notes for Claude working in this repo…"
        value={context}
        onChange={(e) => {
          setContext(e.target.value);
          setContextDirty(true);
        }}
        rows={8}
      />
      <div className="reposkills-editor-actions">
        <button type="button" className="auto-btn" disabled={!contextDirty} onClick={saveContext}>
          Save context
        </button>
      </div>
    </div>
  );
}

// Local PUT helper (the api client has no PUT).
function putJSON(path: string, body: unknown): Promise<void> {
  return fetch(path, {
    method: "PUT",
    credentials: "same-origin",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  }).then((r) => {
    if (!r.ok) throw new Error(`HTTP ${r.status}`);
  });
}
