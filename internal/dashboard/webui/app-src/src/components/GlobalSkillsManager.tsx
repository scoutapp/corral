import { useCallback, useEffect, useState } from "react";
import { getJSON, postJSON, putJSON, delJSON } from "../api/client";

// GlobalSkillsManager — the shared catalog of global skills, shown on the
// Automations page. A global skill can be injected into EVERY repo's sandbox by
// default ("Add to all repos"); a repo can still override that per-skill in its
// own Settings. Backed by /api/skills?scope=global.

type GlobalSkill = { id: number; name: string; content: string; autoAll: boolean };

// The template's description IS an exemplar, not a fill-in-the-blank: it models
// the pattern that actually makes a skill fire — say what it does AND name the
// situation to reach for it ("Use this when…"). The description is the one line
// the model matches your request against to decide whether to load the skill, so
// a vague topic label ("commit conventions") loses; a named situation wins.
const SKILL_TEMPLATE =
  "---\n" +
  "name: my-skill\n" +
  "description: >-\n" +
  "  One line on what this does. Use this when <the exact situation that should\n" +
  "  trigger it — the task, file type, or error that means this knowledge applies>.\n" +
  "---\n\n" +
  "# Instructions\n\n" +
  "Keep the body a short runbook: the exact commands, the failure to watch for,\n" +
  "the fix. Only what the model wouldn't already know.\n";

export function GlobalSkillsManager() {
  const [skills, setSkills] = useState<GlobalSkill[]>([]);
  const [editing, setEditing] = useState<number | "new" | null>(null);
  const [draftName, setDraftName] = useState("");
  const [draftContent, setDraftContent] = useState("");
  const [draftAutoAll, setDraftAutoAll] = useState(true);
  const [msg, setMsg] = useState<{ text: string; err: boolean } | null>(null);

  const load = useCallback(() => {
    getJSON<{ skills: GlobalSkill[] }>("/api/skills?scope=global")
      .then((d) => setSkills(d.skills || []))
      .catch((e) => setMsg({ text: (e as Error).message, err: true }));
  }, []);
  useEffect(() => load(), [load]);

  function startNew() {
    setEditing("new");
    setDraftName("");
    setDraftContent(SKILL_TEMPLATE);
    setDraftAutoAll(true);
  }
  function startEdit(sk: GlobalSkill) {
    setEditing(sk.id);
    setDraftName(sk.name);
    setDraftContent(sk.content);
    setDraftAutoAll(sk.autoAll);
  }

  async function save() {
    if (!draftName.trim() || !draftContent.trim()) {
      setMsg({ text: "A name and SKILL.md content are required.", err: true });
      return;
    }
    try {
      if (editing === "new") {
        await postJSON("/api/skills", {
          scope: "global",
          name: draftName.trim(),
          content: draftContent,
          autoAll: draftAutoAll,
        });
      } else if (typeof editing === "number") {
        await putJSON(`/api/skills/${editing}`, {
          name: draftName.trim(),
          content: draftContent,
          autoAll: draftAutoAll,
        });
      }
      setEditing(null);
      setMsg({ text: `Saved "${draftName.trim()}".`, err: false });
      load();
    } catch (e) {
      setMsg({ text: `Couldn't save: ${(e as Error).message}`, err: true });
    }
  }

  async function remove(sk: GlobalSkill) {
    if (!confirm(`Delete the global "${sk.name}" skill? It will be removed from every repo that uses it.`)) return;
    try {
      await delJSON(`/api/skills/${sk.id}`);
      load();
    } catch (e) {
      setMsg({ text: `Couldn't delete: ${(e as Error).message}`, err: true });
    }
  }

  return (
    <section className="auto-section">
      <div className="reposkills-head">
        <h2 className="auto-mgr-h" style={{ marginTop: 0 }}>
          Global skills
        </h2>
        {editing === null && (
          <button type="button" className="auto-btn" onClick={startNew}>
            + New global skill
          </button>
        )}
      </div>
      <p className="auto-intro" style={{ marginTop: 0 }}>
        Skills shared across all repos. Turn on <b>Add to all repos</b> to inject a skill into every sandbox
        by default; each repo can still toggle it on or off in its own Settings.
      </p>

      {msg && <div className={`auto-msg${msg.err ? " err" : ""}`}>{msg.text}</div>}

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
            rows={12}
          />
          <p className="auto-hint">
            The <code>description</code> is the one line the model matches to decide whether to load this skill. Say
            what it does <b>and</b> name the situation — “Use this when…”. A topic label loses; a named situation fires.
          </p>
          <label className="reposkills-autoall">
            <input type="checkbox" checked={draftAutoAll} onChange={(e) => setDraftAutoAll(e.target.checked)} />{" "}
            Add to all repos by default
          </label>
          <div className="reposkills-editor-actions">
            <button type="button" className="auto-btn" onClick={save}>
              Save skill
            </button>
            <button type="button" className="auto-btn link" onClick={() => setEditing(null)}>
              Cancel
            </button>
          </div>
        </div>
      ) : skills.length === 0 ? (
        <p className="auto-empty">No global skills yet. Add one to share it across every repo.</p>
      ) : (
        <ul className="reposkills-list">
          {skills.map((sk) => (
            <li key={sk.id} className="reposkills-item">
              <span className="reposkills-name">
                {sk.name}
                {sk.autoAll && <span className="reposkills-badge">all repos</span>}
              </span>
              <div className="reposkills-item-actions">
                <button type="button" className="auto-btn link" onClick={() => startEdit(sk)}>
                  edit
                </button>
                <button type="button" className="reposkills-x" title="Delete" onClick={() => remove(sk)}>
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
