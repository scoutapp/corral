import { useCallback, useEffect, useState } from "react";
import { getJSON, postJSON } from "../api/client";
import { BashStepEditor } from "./BashStepEditor";

// ScriptsLibrary is the saved-scripts surface (the Automations "Scripts" tab and,
// scoped, a repo's Settings). A saved script is just a named bash action, so it's
// reusable everywhere actions are: pick it from a "run a script" step, or add it
// to a flow. This component lists them and lets you create/edit/save one with the
// full BashStepEditor (host-run callout, test-run, AI drafting).

type Action = { id: number; name: string; kind: string; scope: string; spec: string };

function scriptOf(a: Action): string {
  try {
    return (JSON.parse(a.spec)?.script as string) || "";
  } catch {
    return "";
  }
}

export function ScriptsLibrary({ repoId }: { repoId?: string }) {
  const scoped = !!repoId;
  const repoQ = scoped ? `?repo=${encodeURIComponent(repoId!)}` : "";

  const [scripts, setScripts] = useState<Action[]>([]);
  const [msg, setMsg] = useState<{ text: string; err: boolean } | null>(null);

  // Editor state: editing an existing script (id) or composing a new one.
  const [editId, setEditId] = useState<number | null>(null);
  const [name, setName] = useState("");
  const [script, setScript] = useState("");
  const [open, setOpen] = useState(false);

  const load = useCallback(() => {
    getJSON<{ actions: Action[] }>(`/api/actions${repoQ}`)
      .then((d) => setScripts((d.actions || []).filter((a) => a.kind === "bash")))
      .catch((e) => setMsg({ text: (e as Error).message, err: true }));
  }, [repoQ]);

  useEffect(() => {
    load();
  }, [load]);

  const startNew = () => {
    setEditId(null);
    setName("");
    setScript(DEFAULT_SCRIPT);
    setOpen(true);
  };

  const startEdit = (a: Action) => {
    setEditId(a.id);
    setName(a.name);
    setScript(scriptOf(a));
    setOpen(true);
  };

  const save = async () => {
    if (!name.trim()) {
      setMsg({ text: "give the script a name", err: true });
      return;
    }
    const spec = JSON.stringify({ script });
    try {
      if (editId != null) {
        await fetch(`/api/actions/${editId}`, {
          method: "PUT",
          credentials: "same-origin",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ name, spec }),
        });
      } else {
        await postJSON("/api/actions", {
          name,
          kind: "bash",
          spec,
          scope: scoped ? "repo" : "global",
          repoId: scoped ? repoId : undefined,
        });
      }
      setMsg({ text: "✓ script saved", err: false });
      setOpen(false);
      load();
    } catch (e) {
      setMsg({ text: (e as Error).message, err: true });
    }
  };

  const del = async (id: number) => {
    await fetch(`/api/actions/${id}`, { method: "DELETE", credentials: "same-origin" }).catch(() => {});
    load();
  };

  return (
    <div className="scripts-lib">
      {msg && <div className={`auto-msg${msg.err ? " err" : ""}`}>{msg.text}</div>}
      <p className="auto-hint" style={{ opacity: 0.85 }}>
        Reusable bash scripts. Save one here, then pick it from a "run a script" step or add it to a
        flow. Scripts run on your host machine (see the callout in the editor).
      </p>

      {scripts.length === 0 ? (
        <p className="auto-empty">No saved scripts yet.</p>
      ) : (
        <table className="auto-table">
          <thead>
            <tr>
              <th>Name</th>
              <th>Scope</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            {scripts.map((a) => (
              <tr key={a.id}>
                <td>{a.name}</td>
                <td>{a.scope}</td>
                <td>
                  {(!scoped || a.scope === "repo") && (
                    <>
                      <button type="button" className="auto-btn link" onClick={() => startEdit(a)}>
                        edit
                      </button>
                      <button type="button" className="auto-del" onClick={() => del(a.id)}>
                        delete
                      </button>
                    </>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}

      {open ? (
        <div className="auto-create">
          <h4 className="auto-mgr-h">{editId != null ? "Edit script" : "New script"}</h4>
          <input
            className="auto-input"
            style={{ width: "100%", marginBottom: "0.4rem" }}
            placeholder="script name"
            value={name}
            onChange={(e) => setName(e.target.value)}
          />
          <BashStepEditor script={script} onChange={setScript} />
          <div className="auto-row" style={{ marginTop: "0.5rem" }}>
            <button type="button" className="auto-btn" onClick={save}>
              Save script
            </button>
            <button type="button" className="auto-btn" onClick={() => setOpen(false)}>
              Cancel
            </button>
          </div>
        </div>
      ) : (
        <button type="button" className="auto-btn" onClick={startNew} style={{ marginTop: "0.5rem" }}>
          + New script
        </button>
      )}
    </div>
  );
}

const DEFAULT_SCRIPT = `#!/usr/bin/env bash
# Runs on the machine hosting this dashboard (NOT the sandbox), with your
# shell environment and any CLIs you're already signed in to (gh, aws, …).
# Event details arrive as env vars: $CORRAL_PR_NUMBER, $CORRAL_PR_URL, etc.
set -euo pipefail

echo "hello from a saved script"
`;
