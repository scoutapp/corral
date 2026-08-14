import { useCallback, useEffect, useState } from "react";
import { getJSON, postJSON } from "../api/client";

// AutomationsManager is the shared actions + hooks editor used by both the
// global Automations page (repoId undefined → global scope) and a repo's
// Settings tab (repoId set → repo scope, seeing its own + global). It talks to
// the same /api/* control plane, passing ?repo= when repo-scoped.

type Action = { id: number; name: string; kind: string; scope: string; repoId?: string };
type Hook = { id: number; event: string; scope: string; targetKind: string; targetId: number };
type FlowStep = { id: number; actionId: number; stepKey: string; position: number };
type Flow = { id: number; name: string; scope: string; steps?: FlowStep[] };

const KINDS = [
  { kind: "capability", label: "PR capability (gh)" },
  { kind: "slack", label: "Slack message" },
  { kind: "webhook", label: "Webhook (HTTP POST)" },
  { kind: "claude_prompt", label: "Prompt template" },
  { kind: "bash", label: "Bash script" },
];

const EVENTS = [
  "pr.approve",
  "pr.comment",
  "pr.request_changes",
  "pr.merge",
  "pr.analyze",
  "pr.enter",
  "project.start",
];

function starterSpec(kind: string): string {
  switch (kind) {
    case "capability":
      return JSON.stringify({ capability: "pr-approve", body: "" }, null, 2);
    case "slack":
      return JSON.stringify({ webhookUrl: "{{secret.slack_hook}}", message: "PR {{pr_number}}: {{pr_title}}" }, null, 2);
    case "webhook":
      return JSON.stringify({ url: "", body: '{"pr":"{{pr_number}}"}' }, null, 2);
    case "claude_prompt":
      return JSON.stringify({ template: "Work on {{repo}} @ {{branch}}." }, null, 2);
    case "bash":
      return JSON.stringify({ script: 'echo "PR $CORRAL_PR_NUMBER"' }, null, 2);
    default:
      return "{}";
  }
}

export function AutomationsManager({ repoId }: { repoId?: string }) {
  const scoped = !!repoId;
  const repoQ = scoped ? `?repo=${encodeURIComponent(repoId!)}` : "";

  const [actions, setActions] = useState<Action[]>([]);
  const [hooks, setHooks] = useState<Record<string, Hook[]>>({});
  const [msg, setMsg] = useState<{ text: string; err: boolean } | null>(null);

  const [name, setName] = useState("");
  const [kind, setKind] = useState("slack");
  const [spec, setSpec] = useState(starterSpec("slack"));

  const [hookEvent, setHookEvent] = useState("pr.approve");
  // Encoded as "action:<id>" | "flow:<id>" so a hook can target either.
  const [hookTarget, setHookTarget] = useState<string>("");

  // Flows.
  const [flows, setFlows] = useState<Flow[]>([]);
  const [flowName, setFlowName] = useState("");
  const [stepAction, setStepAction] = useState<Record<number, number | "">>({});

  const loadActions = useCallback(() => {
    getJSON<{ actions: Action[] }>(`/api/actions${repoQ}`)
      .then((d) => setActions(d.actions || []))
      .catch((e) => setMsg({ text: (e as Error).message, err: true }));
  }, [repoQ]);

  const loadHooks = useCallback(() => {
    Promise.all(
      EVENTS.map((ev) =>
        getJSON<{ hooks: Hook[] }>(`/api/hooks?event=${encodeURIComponent(ev)}${scoped ? `&repo=${encodeURIComponent(repoId!)}` : ""}`)
          .then((d) => [ev, d.hooks || []] as const)
          .catch(() => [ev, []] as const),
      ),
    ).then((pairs) => setHooks(Object.fromEntries(pairs)));
  }, [scoped, repoId]);

  const loadFlows = useCallback(() => {
    getJSON<{ flows: Flow[] }>(`/api/flows${repoQ}`)
      .then(async (d) => {
        // Fetch each flow's steps for display.
        const full = await Promise.all(
          (d.flows || []).map((f) => getJSON<Flow>(`/api/flows/${f.id}`).catch(() => f)),
        );
        setFlows(full);
      })
      .catch(() => {});
  }, [repoQ]);

  useEffect(() => {
    loadActions();
    loadHooks();
    loadFlows();
  }, [loadActions, loadHooks, loadFlows]);

  const pickKind = (k: string) => {
    setKind(k);
    setSpec(starterSpec(k));
  };

  const createAction = async () => {
    if (!name.trim()) {
      setMsg({ text: "name is required", err: true });
      return;
    }
    try {
      await postJSON("/api/actions", {
        name,
        kind,
        spec,
        scope: scoped ? "repo" : "global",
        repoId: scoped ? repoId : undefined,
      });
      setName("");
      setSpec(starterSpec(kind));
      setMsg({ text: "✓ action created", err: false });
      loadActions();
    } catch (e) {
      setMsg({ text: (e as Error).message, err: true });
    }
  };

  const deleteAction = async (id: number) => {
    await fetch(`/api/actions/${id}`, { method: "DELETE", credentials: "same-origin" }).catch(() => {});
    loadActions();
    loadHooks();
  };

  const bindHook = async () => {
    if (!hookTarget) {
      setMsg({ text: "choose an action or flow to bind", err: true });
      return;
    }
    const [targetKind, idStr] = hookTarget.split(":");
    try {
      await postJSON("/api/hooks", {
        event: hookEvent,
        scope: scoped ? "repo" : "global",
        repoId: scoped ? repoId : undefined,
        targetKind,
        targetId: Number(idStr),
        enabled: true,
      });
      setMsg({ text: "✓ hook bound", err: false });
      loadHooks();
    } catch (e) {
      setMsg({ text: (e as Error).message, err: true });
    }
  };

  const unbindHook = async (id: number) => {
    await fetch(`/api/hooks/${id}`, { method: "DELETE", credentials: "same-origin" }).catch(() => {});
    loadHooks();
  };

  const createFlow = async () => {
    if (!flowName.trim()) {
      setMsg({ text: "flow name is required", err: true });
      return;
    }
    try {
      await postJSON("/api/flows", {
        name: flowName,
        scope: scoped ? "repo" : "global",
        repoId: scoped ? repoId : undefined,
      });
      setFlowName("");
      setMsg({ text: "✓ flow created", err: false });
      loadFlows();
    } catch (e) {
      setMsg({ text: (e as Error).message, err: true });
    }
  };

  const addStep = async (flowId: number, position: number) => {
    const actionId = stepAction[flowId];
    if (!actionId) return;
    const a = actions.find((x) => x.id === actionId);
    const stepKey = (a?.name || `step${position}`).replace(/[^a-zA-Z0-9]/g, "_").toLowerCase();
    await postJSON(`/api/flows/${flowId}/steps`, { actionId, position, stepKey }).catch((e) =>
      setMsg({ text: (e as Error).message, err: true }),
    );
    setStepAction((s) => ({ ...s, [flowId]: "" }));
    loadFlows();
  };

  const deleteFlow = async (id: number) => {
    await fetch(`/api/flows/${id}`, { method: "DELETE", credentials: "same-origin" }).catch(() => {});
    loadFlows();
  };

  const actionName = (id: number) => actions.find((a) => a.id === id)?.name || `#${id}`;
  const targetName = (h: Hook) =>
    h.targetKind === "flow"
      ? `${flows.find((f) => f.id === h.targetId)?.name || `#${h.targetId}`} (flow)`
      : actionName(h.targetId);

  return (
    <div className="auto-manager">
      {msg && <div className={`auto-msg${msg.err ? " err" : ""}`}>{msg.text}</div>}

      <h3 className="auto-mgr-h">{scoped ? "Repo actions" : "Action catalog"}</h3>
      {actions.length === 0 ? (
        <p className="auto-empty">
          No actions yet{scoped ? " for this repo (global ones still apply)" : ""}. Create one below.
        </p>
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
                  {/* Only repo-scoped actions are deletable from a repo view. */}
                  {(!scoped || a.scope === "repo") && (
                    <button type="button" className="auto-del" onClick={() => deleteAction(a.id)}>
                      delete
                    </button>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}

      <div className="auto-create">
        <h4 className="auto-mgr-h">New {scoped ? "repo " : ""}action</h4>
        <div className="auto-row">
          <input className="auto-input" placeholder="name" value={name} onChange={(e) => setName(e.target.value)} />
          <select className="auto-input" value={kind} onChange={(e) => pickKind(e.target.value)}>
            {KINDS.map((k) => (
              <option key={k.kind} value={k.kind}>
                {k.label}
              </option>
            ))}
          </select>
        </div>
        <textarea className="auto-spec" rows={7} value={spec} onChange={(e) => setSpec(e.target.value)} spellCheck={false} />
        {(kind === "slack" || kind === "webhook") && (
          <p className="auto-hint">
            Reference secrets as <code>{"{{secret.NAME}}"}</code> (resolved from the credential store); the target host
            must be on the firewall allowlist.
          </p>
        )}
        <button type="button" className="auto-btn" onClick={createAction}>
          Create action
        </button>
      </div>

      <h3 className="auto-mgr-h">Event hooks{scoped ? " (this repo)" : ""}</h3>
      <div className="auto-row">
        <select className="auto-input" value={hookEvent} onChange={(e) => setHookEvent(e.target.value)}>
          {EVENTS.map((ev) => (
            <option key={ev} value={ev}>
              {ev}
            </option>
          ))}
        </select>
        <select className="auto-input" value={hookTarget} onChange={(e) => setHookTarget(e.target.value)}>
          <option value="">choose action or flow…</option>
          {actions.length > 0 && (
            <optgroup label="Actions">
              {actions.map((a) => (
                <option key={`a${a.id}`} value={`action:${a.id}`}>
                  {a.name} ({a.kind})
                </option>
              ))}
            </optgroup>
          )}
          {flows.length > 0 && (
            <optgroup label="Flows">
              {flows.map((f) => (
                <option key={`f${f.id}`} value={`flow:${f.id}`}>
                  {f.name} (flow)
                </option>
              ))}
            </optgroup>
          )}
        </select>
        <button type="button" className="auto-btn" onClick={bindHook}>
          Bind
        </button>
      </div>

      {EVENTS.map((ev) => {
        const bound = hooks[ev] || [];
        if (bound.length === 0) return null;
        return (
          <div key={ev} className="auto-event">
            <div className="auto-event-name">
              <code>{ev}</code>
            </div>
            <ul className="auto-hooklist">
              {bound.map((h) => (
                <li key={h.id}>
                  → {targetName(h)} <span className="auto-scope">{h.scope}</span>
                  {/* Global hooks aren't removable from a repo view. */}
                  {(!scoped || h.scope === "repo") && (
                    <button type="button" className="auto-del" onClick={() => unbindHook(h.id)}>
                      remove
                    </button>
                  )}
                </li>
              ))}
            </ul>
          </div>
        );
      })}

      <h3 className="auto-mgr-h">Flows{scoped ? " (this repo)" : ""}</h3>
      <p className="auto-hint" style={{ opacity: 0.8 }}>
        A flow runs its actions in order; a later step can use an earlier one's result via{" "}
        <code>{"{{steps.KEY.output}}"}</code>. Bind a flow to an event just like an action.
      </p>
      <div className="auto-row">
        <input
          className="auto-input"
          placeholder="new flow name"
          value={flowName}
          onChange={(e) => setFlowName(e.target.value)}
        />
        <button type="button" className="auto-btn" onClick={createFlow}>
          Create flow
        </button>
      </div>

      {flows.map((f) => (
        <div key={f.id} className="auto-flow">
          <div className="auto-flow-head">
            <b>{f.name}</b> <span className="auto-scope">{f.scope}</span>
            {(!scoped || f.scope === "repo") && (
              <button type="button" className="auto-del" onClick={() => deleteFlow(f.id)}>
                delete
              </button>
            )}
          </div>
          <ol className="auto-flow-steps">
            {(f.steps || []).map((st) => (
              <li key={st.id}>
                {actionName(st.actionId)} <span className="auto-step-key">{st.stepKey}</span>
              </li>
            ))}
            {(f.steps || []).length === 0 && <li className="auto-empty">no steps yet</li>}
          </ol>
          <div className="auto-row">
            <select
              className="auto-input"
              value={stepAction[f.id] ?? ""}
              onChange={(e) =>
                setStepAction((s) => ({ ...s, [f.id]: e.target.value ? Number(e.target.value) : "" }))
              }
            >
              <option value="">add step…</option>
              {actions.map((a) => (
                <option key={a.id} value={a.id}>
                  {a.name} ({a.kind})
                </option>
              ))}
            </select>
            <button type="button" className="auto-btn" onClick={() => addStep(f.id, (f.steps || []).length)}>
              Add step
            </button>
          </div>
        </div>
      ))}
    </div>
  );
}
