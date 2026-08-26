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
          {editId != null && <ScriptSecrets actionId={editId} script={script} />}
          <div className="auto-row" style={{ marginTop: "0.5rem" }}>
            <button type="button" className="auto-btn" onClick={save}>
              Save script
            </button>
            <button type="button" className="auto-btn" onClick={() => setOpen(false)}>
              Cancel
            </button>
          </div>
          {editId == null && (
            <p className="auto-hint" style={{ opacity: 0.7, marginTop: "0.4rem" }}>
              Save the script first, then re-open it to set secrets (API keys, etc.) it needs.
            </p>
          )}
        </div>
      ) : (
        <button type="button" className="auto-btn" onClick={startNew} style={{ marginTop: "0.5rem" }}>
          + New script
        </button>
      )}
    </div>
  );
}

// ScriptSecrets: the per-script secrets form. It shows env vars the script needs
// (detected server-side, merged with any already stored) and lets you set values.
// Values are stored in the Keychain (macOS) and injected into the script's env at
// run time — and stripped from host-claude transcripts. The server never returns a
// stored value (masked only); an empty field means "leave unchanged".
type SecretRow = { name: string; set: boolean; masked?: string };

function ScriptSecrets({ actionId, script }: { actionId: number; script: string }) {
  const [rows, setRows] = useState<SecretRow[]>([]);
  const [vals, setVals] = useState<Record<string, string>>({}); // name -> new value (unsaved)
  const [newName, setNewName] = useState("");
  const [note, setNote] = useState<string | null>(null);

  const load = useCallback(() => {
    getJSON<{ secrets: SecretRow[] }>(`/api/actions/${actionId}/secrets`)
      .then((d) => setRows(d.secrets || []))
      .catch(() => {});
  }, [actionId]);
  useEffect(() => {
    load();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [actionId, script]); // re-detect when the script body changes

  const save = async () => {
    const secrets = Object.entries(vals)
      .filter(([, v]) => v !== "")
      .map(([name, value]) => ({ name, value }));
    if (secrets.length === 0) {
      setNote("nothing to save");
      return;
    }
    try {
      await fetch(`/api/actions/${actionId}/secrets`, {
        method: "PUT",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ secrets }),
      });
      setVals({});
      setNote("✓ secrets saved (stored in your Keychain, injected at run)");
      load();
    } catch (e) {
      setNote((e as Error).message);
    }
  };

  const remove = async (name: string) => {
    await fetch(`/api/actions/${actionId}/secrets`, {
      method: "PUT",
      credentials: "same-origin",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ remove: [name] }),
    }).catch(() => {});
    load();
  };

  const addRow = () => {
    const n = newName.trim().toUpperCase().replace(/[^A-Z0-9_]/g, "_");
    if (n && !rows.some((r) => r.name === n)) {
      setRows((rs) => [...rs, { name: n, set: false }]);
      setNewName("");
    }
  };

  return (
    <div className="script-secrets" style={{ marginTop: "0.6rem", borderTop: "1px solid var(--con-line, #2a3a36)", paddingTop: "0.5rem" }}>
      <div className="auto-mgr-h" style={{ marginBottom: "0.25rem" }}>Secrets</div>
      <p className="auto-hint" style={{ opacity: 0.75, marginTop: 0 }}>
        Env vars this script needs (detected from the body). Values are kept in your Keychain, injected
        into the script's environment when it runs, and stripped from host-Claude conversations.
      </p>
      {rows.length === 0 ? (
        <p className="auto-empty" style={{ opacity: 0.7 }}>No secrets detected. Add one if the script needs an API key.</p>
      ) : (
        <table className="auto-table">
          <tbody>
            {rows.map((r) => (
              <tr key={r.name}>
                <td style={{ fontFamily: "ui-monospace, monospace", whiteSpace: "nowrap" }}>{r.name}</td>
                <td style={{ width: "100%" }}>
                  <input
                    className="auto-input"
                    type="password"
                    style={{ width: "100%" }}
                    placeholder={r.set ? `set (${r.masked}) — leave blank to keep` : "enter value"}
                    value={vals[r.name] ?? ""}
                    onChange={(e) => setVals((v) => ({ ...v, [r.name]: e.target.value }))}
                  />
                </td>
                <td>
                  {r.set && (
                    <button type="button" className="auto-del" onClick={() => remove(r.name)}>
                      remove
                    </button>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      <div className="auto-row" style={{ marginTop: "0.4rem", gap: "0.4rem" }}>
        <input
          className="auto-input"
          style={{ maxWidth: "16rem" }}
          placeholder="+ add a secret var (e.g. FRESHDESK_API_KEY)"
          value={newName}
          onChange={(e) => setNewName(e.target.value)}
          onKeyDown={(e) => e.key === "Enter" && (e.preventDefault(), addRow())}
        />
        <button type="button" className="auto-btn link" onClick={addRow}>add</button>
        <button type="button" className="auto-btn" onClick={save}>Save secrets</button>
      </div>
      {note && <div className="auto-msg" style={{ marginTop: "0.3rem" }}>{note}</div>}
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
