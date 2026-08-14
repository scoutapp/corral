import { useCallback, useEffect, useState } from "react";
import { Link } from "../router";
import { getJSON, postJSON } from "../api/client";
import { useBodyClass } from "../hooks/useBodyClass";

// Top-level Automations page: the global action catalog + event hook bindings.
// This is the "meta" surface — where a user turns hard-coded event bodies into
// their own units of work. Per-repo overrides live in the repo Settings tab;
// this page manages the global defaults + shared catalog.
//
// Everything here talks to the API-first /api/* control plane, so the same
// operations are reachable from a future CLI/macros.

type Action = {
  id: number;
  name: string;
  kind: string;
  spec: string;
  scope: string;
  repoId?: string;
};
type Hook = {
  id: number;
  event: string;
  scope: string;
  targetKind: string;
  targetId: number;
  enabled: boolean;
};

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

// A starter spec per kind so the create form shows the right shape.
function starterSpec(kind: string): string {
  switch (kind) {
    case "capability":
      return JSON.stringify({ capability: "pr-approve", body: "" }, null, 2);
    case "slack":
      return JSON.stringify({ webhookUrl: "", message: "PR {{pr_number}}: {{pr_title}}" }, null, 2);
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

export function AutomationsPage() {
  useBodyClass("console");
  const [actions, setActions] = useState<Action[]>([]);
  const [hooks, setHooks] = useState<Record<string, Hook[]>>({});
  const [msg, setMsg] = useState<{ text: string; err: boolean } | null>(null);

  // Create-action form state.
  const [name, setName] = useState("");
  const [kind, setKind] = useState("slack");
  const [spec, setSpec] = useState(starterSpec("slack"));

  // Bind-hook form state.
  const [hookEvent, setHookEvent] = useState("pr.approve");
  const [hookTarget, setHookTarget] = useState<number | "">("");

  const loadActions = useCallback(() => {
    getJSON<{ actions: Action[] }>("/api/actions")
      .then((d) => setActions(d.actions || []))
      .catch((e) => setMsg({ text: (e as Error).message, err: true }));
  }, []);

  const loadHooks = useCallback(() => {
    Promise.all(
      EVENTS.map((ev) =>
        getJSON<{ hooks: Hook[] }>(`/api/hooks?event=${encodeURIComponent(ev)}`)
          .then((d) => [ev, d.hooks || []] as const)
          .catch(() => [ev, []] as const),
      ),
    ).then((pairs) => setHooks(Object.fromEntries(pairs)));
  }, []);

  useEffect(() => {
    loadActions();
    loadHooks();
  }, [loadActions, loadHooks]);

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
      await postJSON("/api/actions", { name, kind, spec, scope: "global" });
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
      setMsg({ text: "choose an action to bind", err: true });
      return;
    }
    try {
      await postJSON("/api/hooks", {
        event: hookEvent,
        targetKind: "action",
        targetId: Number(hookTarget),
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

  const actionName = (id: number) => actions.find((a) => a.id === id)?.name || `#${id}`;

  return (
    <>
      <header className="console-header">
        <div className="brand">
          <Link to="/" className="back">
            ← All repos
          </Link>
          <span className="brand-name">Automations</span>
          <Link to="/global" className="brand-sub">
            global settings →
          </Link>
        </div>
      </header>

      <div className="auto-page">
        <p className="auto-intro">
          Turn events into your own units of work. Actions are reusable steps; hooks bind an action to
          an event (like approving a PR) so it also runs. Built-in behavior always runs first — hooks
          are additive and best-effort. These are <b>global</b> defaults; per-repo overrides live in a
          repo's Settings tab.
        </p>

        {msg && <div className={`auto-msg${msg.err ? " err" : ""}`}>{msg.text}</div>}

        <section className="auto-section">
          <h2>Action catalog</h2>
          {actions.length === 0 ? (
            <p className="auto-empty">No actions yet. Create one below.</p>
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
                      <button type="button" className="auto-del" onClick={() => deleteAction(a.id)}>
                        delete
                      </button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}

          <div className="auto-create">
            <h3>New action</h3>
            <div className="auto-row">
              <input
                className="auto-input"
                placeholder="name"
                value={name}
                onChange={(e) => setName(e.target.value)}
              />
              <select className="auto-input" value={kind} onChange={(e) => pickKind(e.target.value)}>
                {KINDS.map((k) => (
                  <option key={k.kind} value={k.kind}>
                    {k.label}
                  </option>
                ))}
              </select>
            </div>
            <textarea
              className="auto-spec"
              rows={7}
              value={spec}
              onChange={(e) => setSpec(e.target.value)}
              spellCheck={false}
            />
            {(kind === "slack" || kind === "webhook") && (
              <p className="auto-hint">
                Secrets (webhook URLs, tokens) should be injected via the credential proxy, and the
                target host must be on the firewall allowlist.
              </p>
            )}
            <button type="button" className="auto-btn" onClick={createAction}>
              Create action
            </button>
          </div>
        </section>

        <section className="auto-section">
          <h2>Event hooks</h2>
          <div className="auto-row">
            <select className="auto-input" value={hookEvent} onChange={(e) => setHookEvent(e.target.value)}>
              {EVENTS.map((ev) => (
                <option key={ev} value={ev}>
                  {ev}
                </option>
              ))}
            </select>
            <select
              className="auto-input"
              value={hookTarget}
              onChange={(e) => setHookTarget(e.target.value ? Number(e.target.value) : "")}
            >
              <option value="">choose action…</option>
              {actions.map((a) => (
                <option key={a.id} value={a.id}>
                  {a.name} ({a.kind})
                </option>
              ))}
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
                      → {actionName(h.targetId)} <span className="auto-scope">{h.scope}</span>
                      <button type="button" className="auto-del" onClick={() => unbindHook(h.id)}>
                        remove
                      </button>
                    </li>
                  ))}
                </ul>
              </div>
            );
          })}
        </section>

        <section className="auto-section">
          <Link to="/automations/runs" className="auto-runs-link">
            View run history →
          </Link>
        </section>
      </div>
    </>
  );
}
