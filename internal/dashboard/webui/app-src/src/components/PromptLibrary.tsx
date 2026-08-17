import { useCallback, useEffect, useState } from "react";
import { getJSON, postJSON, delJSON } from "../api/client";

// PromptLibrary — the user's SAVED prompts, a companion to the fixed-catalog
// carousel above it. Save a prompt by name once, then pick it when starting a
// project or working an issue. Backed by /api/prompts/library (claude_prompt
// actions whose name isn't a reserved catalog key). Scoped global on the
// Automations page, or to a repo in that repo's Settings.

type NamedPrompt = {
  id: number;
  name: string;
  template: string;
  description?: string;
  scope: "global" | "repo";
  repoId?: string;
};

export function PromptLibrary({ repoId, onMsg }: { repoId?: string; onMsg?: (m: { text: string; err: boolean }) => void }) {
  const scopeQ = repoId ? `?repo=${encodeURIComponent(repoId)}` : "";
  const [prompts, setPrompts] = useState<NamedPrompt[]>([]);
  const [editing, setEditing] = useState<number | "new" | null>(null);
  const [draftName, setDraftName] = useState("");
  const [draftTemplate, setDraftTemplate] = useState("");
  const [draftDesc, setDraftDesc] = useState("");

  const load = useCallback(() => {
    getJSON<{ prompts: NamedPrompt[] }>(`/api/prompts/library${scopeQ}`)
      .then((d) => setPrompts(d.prompts || []))
      .catch((e) => onMsg?.({ text: (e as Error).message, err: true }));
  }, [scopeQ, onMsg]);
  useEffect(() => load(), [load]);

  function startNew() {
    setEditing("new");
    setDraftName("");
    setDraftTemplate("");
    setDraftDesc("");
  }
  function startEdit(p: NamedPrompt) {
    setEditing(p.id);
    setDraftName(p.name);
    setDraftTemplate(p.template);
    setDraftDesc(p.description || "");
  }

  async function save() {
    if (!draftName.trim() || !draftTemplate.trim()) {
      onMsg?.({ text: "A name and a template are required.", err: true });
      return;
    }
    try {
      if (editing === "new") {
        await postJSON("/api/prompts/library", {
          name: draftName.trim(),
          template: draftTemplate,
          description: draftDesc.trim(),
          repo: repoId || undefined,
        });
        onMsg?.({ text: `Saved "${draftName.trim()}".`, err: false });
      } else if (typeof editing === "number") {
        await putJSON(`/api/prompts/library/${editing}`, {
          name: draftName.trim(),
          template: draftTemplate,
          description: draftDesc.trim(),
        });
        onMsg?.({ text: `Updated "${draftName.trim()}".`, err: false });
      }
      setEditing(null);
      load();
    } catch (e) {
      onMsg?.({ text: `Couldn't save: ${(e as Error).message}`, err: true });
    }
  }

  async function remove(p: NamedPrompt) {
    if (!confirm(`Delete the "${p.name}" prompt?`)) return;
    try {
      await delJSON(`/api/prompts/library/${p.id}`);
      load();
    } catch (e) {
      onMsg?.({ text: `Couldn't delete: ${(e as Error).message}`, err: true });
    }
  }

  return (
    <section className="promptlib">
      <div className="promptlib-head">
        <h3 className="auto-mgr-h" style={{ margin: 0 }}>
          Saved prompts <span className="auto-hint" style={{ fontWeight: 400 }}>— pick these when you start a project or work an issue</span>
        </h3>
        {editing === null && (
          <button type="button" className="auto-btn" onClick={startNew}>
            + New prompt
          </button>
        )}
      </div>

      {editing !== null ? (
        <div className="promptlib-editor">
          <input
            className="auto-input"
            placeholder="Name (e.g. Thorough refactor)"
            value={draftName}
            onChange={(e) => setDraftName(e.target.value)}
          />
          <input
            className="auto-input"
            placeholder="Short description (optional)"
            value={draftDesc}
            onChange={(e) => setDraftDesc(e.target.value)}
          />
          <textarea
            className="auto-input promptlib-template"
            placeholder="The prompt text. Use {{repo}}, {{branch}}, {{number}}, {{title}} where the start flow fills them in."
            value={draftTemplate}
            onChange={(e) => setDraftTemplate(e.target.value)}
            rows={6}
          />
          <div className="promptlib-editor-actions">
            <button type="button" className="auto-btn" onClick={save}>
              Save
            </button>
            <button type="button" className="auto-btn link" onClick={() => setEditing(null)}>
              Cancel
            </button>
          </div>
        </div>
      ) : prompts.length === 0 ? (
        <p className="auto-empty">No saved prompts yet. Create one to reuse it at project or issue start.</p>
      ) : (
        <ul className="promptlib-list">
          {prompts.map((p) => (
            <li key={p.id} className="promptlib-item">
              <div className="promptlib-item-main">
                <span className="promptlib-name">{p.name}</span>
                {p.scope === "repo" && <span className="promptlib-scope">this repo</span>}
                {p.description && <span className="promptlib-desc">{p.description}</span>}
                <div className="promptlib-preview">{p.template}</div>
              </div>
              <div className="promptlib-item-actions">
                <button type="button" className="auto-btn link" onClick={() => startEdit(p)}>
                  edit
                </button>
                <button type="button" className="promptlib-x" title="Delete" onClick={() => remove(p)}>
                  ×
                </button>
              </div>
            </li>
          ))}
        </ul>
      )}
    </section>
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
