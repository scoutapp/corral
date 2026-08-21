import { useCallback, useEffect, useState } from "react";
import { getJSON, postJSON } from "../api/client";
import { PromptsCarousel } from "./PromptsCarousel";
import { PromptLibrary } from "./PromptLibrary";
import { BashStepEditor } from "./BashStepEditor";

// AutomationsManager is the approachable actions/automations editor used by the
// global Automations page (repoId undefined → global) and a repo's Settings tab
// (repoId set → repo scope). It's organized around what a user actually thinks
// about — not the underlying event/hook model:
//
//   1. Prompts    — the project-start prompt, edited inline (+ Build with AI).
//   2. Automations — one card per trigger ("When you approve a PR"): the
//                    built-in step, then any extra steps the user added, with a
//                    plain "+ Add a step" that creates the hook under the hood.
//   3. Advanced    — the raw action catalog, collapsed.
//
// Everything still talks to the /api/* control plane; the words "event" and
// "hook" never appear in the UI.

type Action = { id: number; name: string; kind: string; scope: string; repoId?: string };
type Hook = { id: number; event: string; scope: string; targetKind: string; targetId: number };
type Trigger = {
  event: string;
  title: string;
  verb: string;
  description: string;
  builtin: string;
  builtinIcon: string;
};

// Step kinds a user can add from a card, kept to the approachable few. The raw
// capability/prompt kinds live in Advanced. "Run a script" is first — it's the
// most flexible and the one people reach for.
const STEP_KINDS = [
  { kind: "bash", label: "Run a script", starter: { script: "" } },
  { kind: "slack", label: "Send a Slack message", starter: { webhookUrl: "{{secret.slack_hook}}", message: "PR {{pr_number}}: {{pr_title}}" } },
  { kind: "webhook", label: "Call a webhook", starter: { url: "", body: '{"pr":"{{pr_number}}"}' } },
];

// Seed script: a comment header explaining the runtime + a trailing newline.
export const DEFAULT_BASH_SCRIPT = `#!/usr/bin/env bash
# Runs on the machine hosting this dashboard (NOT the sandbox), with your
# shell environment and any CLIs you're already signed in to (gh, aws, …).
# Event details arrive as env vars: $CORRAL_PR_NUMBER, $CORRAL_PR_URL, etc.
set -euo pipefail

echo "PR $CORRAL_PR_NUMBER"
`;

export function AutomationsManager({ repoId }: { repoId?: string }) {
  const scoped = !!repoId;
  const repoQ = scoped ? `?repo=${encodeURIComponent(repoId!)}` : "";

  const [actions, setActions] = useState<Action[]>([]);
  const [hooksByEvent, setHooksByEvent] = useState<Record<string, Hook[]>>({});
  const [triggers, setTriggers] = useState<Trigger[]>([]);
  const [msg, setMsg] = useState<{ text: string; err: boolean } | null>(null);

  const loadActions = useCallback(() => {
    getJSON<{ actions: Action[] }>(`/api/actions${repoQ}`)
      .then((d) => setActions(d.actions || []))
      .catch((e) => setMsg({ text: (e as Error).message, err: true }));
  }, [repoQ]);

  const loadTriggers = useCallback(() => {
    getJSON<{ triggers: Trigger[] }>("/api/triggers")
      .then((d) => setTriggers(d.triggers || []))
      .catch(() => {});
  }, []);

  const loadHooks = useCallback(() => {
    getJSON<{ triggers: Trigger[] }>("/api/triggers").then((d) => {
      const evs = (d.triggers || []).map((t) => t.event);
      Promise.all(
        evs.map((ev) =>
          getJSON<{ hooks: Hook[] }>(
            `/api/hooks?event=${encodeURIComponent(ev)}${scoped ? `&repo=${encodeURIComponent(repoId!)}` : ""}`,
          )
            .then((d) => [ev, d.hooks || []] as const)
            .catch(() => [ev, []] as const),
        ),
      ).then((pairs) => setHooksByEvent(Object.fromEntries(pairs)));
    });
  }, [scoped, repoId]);

  useEffect(() => {
    loadActions();
    loadTriggers();
    loadHooks();
  }, [loadActions, loadTriggers, loadHooks]);

  const actionById = (id: number) => actions.find((a) => a.id === id);

  // Add a step to a trigger: create the action, then bind it to the event. The
  // user never sees either term.
  const addStep = async (event: string, kind: string, name: string, spec: string) => {
    try {
      const a = await postJSON<Action>("/api/actions", {
        name,
        kind,
        spec,
        scope: scoped ? "repo" : "global",
        repoId: scoped ? repoId : undefined,
      });
      await postJSON("/api/hooks", {
        event,
        scope: scoped ? "repo" : "global",
        repoId: scoped ? repoId : undefined,
        targetKind: "action",
        targetId: a.id,
        enabled: true,
      });
      setMsg({ text: "✓ step added", err: false });
      loadActions();
      loadHooks();
    } catch (e) {
      setMsg({ text: (e as Error).message, err: true });
    }
  };

  // Bind an EXISTING saved action (e.g. a saved script) to a trigger — no new
  // action is created; the same script is reused.
  const addExistingStep = async (event: string, actionId: number) => {
    try {
      await postJSON("/api/hooks", {
        event,
        scope: scoped ? "repo" : "global",
        repoId: scoped ? repoId : undefined,
        targetKind: "action",
        targetId: actionId,
        enabled: true,
      });
      setMsg({ text: "✓ step added", err: false });
      loadHooks();
    } catch (e) {
      setMsg({ text: (e as Error).message, err: true });
    }
  };

  // Saved bash scripts available to reference from a "run a script" step.
  const savedScripts = actions.filter((a) => a.kind === "bash");

  const removeStep = async (hookId: number, actionId: number, actionScope: string) => {
    await fetch(`/api/hooks/${hookId}`, { method: "DELETE", credentials: "same-origin" }).catch(() => {});
    // Only delete the action too if it's in our scope (don't touch global from a repo view).
    if (!scoped || actionScope === "repo") {
      await fetch(`/api/actions/${actionId}`, { method: "DELETE", credentials: "same-origin" }).catch(() => {});
    }
    loadActions();
    loadHooks();
  };

  return (
    <div className="auto-manager">
      {msg && <div className={`auto-msg${msg.err ? " err" : ""}`}>{msg.text}</div>}

      {/* 1. Prompts — the editable prompt catalog, in its own called-out panel
          (dropdown picker). Global on the Automations page; repo-scoped overrides
          in a repo's Settings tab. */}
      <PromptsCarousel repoId={repoId} onMsg={setMsg} />

      {/* 1b. Saved prompts — the user's named library, picked at project/issue
          start. Companion to the fixed catalog above. */}
      <PromptLibrary repoId={repoId} onMsg={setMsg} />

      {/* 2. Automations — one card per trigger. */}
      <h3 className="auto-mgr-h">Automations{scoped ? " (this repo)" : ""}</h3>
      <p className="auto-hint" style={{ opacity: 0.85 }}>
        Each card is something Corral already does. Add your own steps to run alongside it — a Slack
        ping when you approve, a script when a project starts, and so on.
      </p>

      {triggers.map((tr) => (
        <TriggerCard
          key={tr.event}
          trigger={tr}
          steps={(hooksByEvent[tr.event] || []).filter((h) => h.targetKind === "action")}
          actionById={actionById}
          savedScripts={savedScripts}
          scoped={scoped}
          onAdd={(kind, name, spec) => addStep(tr.event, kind, name, spec)}
          onAddExisting={(actionId) => addExistingStep(tr.event, actionId)}
          onRemove={removeStep}
        />
      ))}

      {/* 3. Advanced — raw actions, collapsed. */}
      <AdvancedSection
        actions={actions}
        scoped={scoped}
        onChanged={() => {
          loadActions();
          loadHooks();
        }}
      />
    </div>
  );
}


// --- Trigger card -----------------------------------------------------------

function TriggerCard({
  trigger,
  steps,
  actionById,
  savedScripts,
  scoped,
  onAdd,
  onAddExisting,
  onRemove,
}: {
  trigger: Trigger;
  steps: Hook[];
  actionById: (id: number) => Action | undefined;
  savedScripts: Action[];
  scoped: boolean;
  onAdd: (kind: string, name: string, spec: string) => void;
  onAddExisting: (actionId: number) => void;
  onRemove: (hookId: number, actionId: number, actionScope: string) => void;
}) {
  const [adding, setAdding] = useState(false);
  const [kind, setKind] = useState(STEP_KINDS[0].kind);
  const [name, setName] = useState("");
  const [spec, setSpec] = useState(JSON.stringify(STEP_KINDS[0].starter, null, 2));
  // Bash steps get a dedicated editor; keep its script separate from the raw
  // JSON. Seed with a helpful comment header + trailing newline.
  const [bashScript, setBashScript] = useState(DEFAULT_BASH_SCRIPT);
  // "run a script": pick a saved script (referenced) or write a new one.
  const [savedId, setSavedId] = useState<number | "new">("new");

  const pickKind = (k: string) => {
    setKind(k);
    const def = STEP_KINDS.find((s) => s.kind === k);
    setSpec(JSON.stringify(def?.starter ?? {}, null, 2));
  };

  const submit = () => {
    // Reference an existing saved script rather than creating a new action.
    if (kind === "bash" && savedId !== "new") {
      onAddExisting(savedId);
      setAdding(false);
      setSavedId("new");
      return;
    }
    const finalSpec = kind === "bash" ? JSON.stringify({ script: bashScript }) : spec;
    onAdd(kind, name.trim() || STEP_KINDS.find((s) => s.kind === kind)?.label || kind, finalSpec);
    setAdding(false);
    setName("");
  };

  return (
    <div className="trigger-card">
      <div className="trigger-card-head">
        <span className="trigger-title">{trigger.title}</span>
        <span className="trigger-desc">{trigger.description}</span>
      </div>
      <ol className="trigger-steps">
        {trigger.builtin ? (
          <li className="trigger-step builtin">
            <span className="trigger-step-ico">{trigger.builtinIcon}</span>
            {trigger.builtin}
            <span className="builtin-pill">built-in</span>
          </li>
        ) : (
          <li className="trigger-step builtin muted-step">
            <span className="trigger-step-ico">•</span>
            {trigger.title.replace(/^When /, "")}
          </li>
        )}
        {steps.map((h) => {
          const a = actionById(h.targetId);
          return (
            <li key={h.id} className="trigger-step">
              <span className="trigger-step-ico">{stepIcon(a?.kind)}</span>
              {a?.name || `#${h.targetId}`}
              {(!scoped || h.scope === "repo") && (
                <button
                  type="button"
                  className="auto-del"
                  onClick={() => onRemove(h.id, h.targetId, a?.scope || "global")}
                >
                  remove
                </button>
              )}
            </li>
          );
        })}
      </ol>

      {adding ? (
        <div className="trigger-add">
          <div className="auto-row">
            <select className="auto-input" value={kind} onChange={(e) => pickKind(e.target.value)}>
              {STEP_KINDS.map((k) => (
                <option key={k.kind} value={k.kind}>
                  {k.label}
                </option>
              ))}
            </select>
            {/* Name is only for a freshly-written step; a saved script keeps its name. */}
            {!(kind === "bash" && savedId !== "new") && (
              <input
                className="auto-input"
                placeholder="name (optional)"
                value={name}
                onChange={(e) => setName(e.target.value)}
              />
            )}
          </div>

          {/* run a script: choose a saved one (if any) or write a new one. */}
          {kind === "bash" && savedScripts.length > 0 && (
            <div className="auto-row">
              <select
                className="auto-input"
                value={savedId}
                onChange={(e) => setSavedId(e.target.value === "new" ? "new" : Number(e.target.value))}
              >
                <option value="new">✎ Write a new script</option>
                <optgroup label="Saved scripts">
                  {savedScripts.map((s) => (
                    <option key={s.id} value={s.id}>
                      {s.name} ({s.scope})
                    </option>
                  ))}
                </optgroup>
              </select>
            </div>
          )}

          {kind === "bash" ? (
            savedId === "new" ? (
              <BashStepEditor script={bashScript} onChange={setBashScript} />
            ) : (
              <p className="auto-hint">Uses the saved script — edit it under the Scripts tab.</p>
            )
          ) : (
            <textarea className="auto-spec" rows={5} value={spec} onChange={(e) => setSpec(e.target.value)} spellCheck={false} />
          )}
          {(kind === "slack" || kind === "webhook") && (
            <p className="auto-hint">
              Reference secrets as <code>{"{{secret.NAME}}"}</code>; the target host must be on the
              firewall allowlist.
            </p>
          )}
          <div className="auto-row">
            <button type="button" className="auto-btn" onClick={submit}>
              Add step
            </button>
            <button type="button" className="auto-btn" onClick={() => setAdding(false)}>
              Cancel
            </button>
          </div>
        </div>
      ) : (
        <button type="button" className="trigger-add-btn" onClick={() => setAdding(true)}>
          + Add a step…
        </button>
      )}
    </div>
  );
}

function stepIcon(kind?: string): string {
  switch (kind) {
    case "slack":
      return "💬";
    case "webhook":
      return "🔗";
    case "bash":
      return "⚙";
    case "capability":
      return "▤";
    case "claude_prompt":
      return "✨";
    default:
      return "•";
  }
}

// --- Advanced (raw actions) -------------------------------------------------

function AdvancedSection({
  actions,
  scoped,
  onChanged,
}: {
  actions: Action[];
  scoped: boolean;
  onChanged: () => void;
}) {
  const del = async (id: number) => {
    await fetch(`/api/actions/${id}`, { method: "DELETE", credentials: "same-origin" }).catch(() => {});
    onChanged();
  };
  return (
    <details className="auto-advanced">
      <summary>Advanced · all actions{scoped ? " (this repo + global)" : ""}</summary>
      {actions.length === 0 ? (
        <p className="auto-empty">No actions yet.</p>
      ) : (
        <table className="auto-table">
          <thead>
            <tr>
              <th>Name</th>
              <th>Kind</th>
              <th>Scope</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            {actions.map((a) => (
              <tr key={a.id}>
                <td>{a.name}</td>
                <td>
                  <code>{a.kind}</code>
                </td>
                <td>{a.scope}</td>
                <td>
                  {(!scoped || a.scope === "repo") && (
                    <button type="button" className="auto-del" onClick={() => del(a.id)}>
                      delete
                    </button>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </details>
  );
}
