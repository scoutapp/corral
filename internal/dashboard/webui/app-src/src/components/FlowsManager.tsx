import { useCallback, useEffect, useState } from "react";
import { getJSON, postJSON, delJSON } from "../api/client";

// FlowsManager — compose and run flows. A flow is an ordered set of steps, each
// wrapping an action; a step can depend on earlier steps by key, and its output
// threads to later steps as {{steps.<key>.output}}. Flows can be run now or put
// on a drift-tolerant schedule. Everything goes through /api/flows.

type Action = { id: number; name: string; kind: string };

type FlowStep = {
  id: number;
  position: number;
  actionId: number;
  stepKey: string;
  dependsOn?: string[];
};

type Flow = {
  id: number;
  name: string;
  steps: FlowStep[];
};

type Schedule = {
  cadenceSeconds: number;
  lastRunAt?: string;
  catchUp: boolean;
  enabled: boolean;
} | null;

type StepResult = { name: string; status: string; output: string; error?: string };
type RunResult = { status: string; steps: StepResult[]; runId: number };

// Friendly cadence presets → seconds.
const CADENCES: { label: string; seconds: number }[] = [
  { label: "every 15 min", seconds: 15 * 60 },
  { label: "hourly", seconds: 60 * 60 },
  { label: "daily", seconds: 24 * 60 * 60 },
  { label: "weekly", seconds: 7 * 24 * 60 * 60 },
];

export function FlowsManager() {
  const [flows, setFlows] = useState<Flow[]>([]);
  const [actions, setActions] = useState<Action[]>([]);
  const [selected, setSelected] = useState<number | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [newName, setNewName] = useState("");

  const load = useCallback(() => {
    getJSON<{ flows: Flow[] }>("/api/flows")
      .then((d) => setFlows(d.flows || []))
      .catch((e) => setErr((e as Error).message));
    getJSON<{ actions: Action[] }>("/api/actions")
      .then((d) => setActions(d.actions || []))
      .catch(() => {});
  }, []);
  useEffect(() => load(), [load]);

  const current = flows.find((f) => f.id === selected) || null;

  async function createFlow() {
    if (!newName.trim()) return;
    setErr(null);
    try {
      const f = await postJSON<Flow>("/api/flows", { name: newName.trim() });
      setNewName("");
      setSelected(f.id);
      load();
    } catch (e) {
      setErr(`Couldn't create flow: ${(e as Error).message}`);
    }
  }

  async function removeFlow(id: number) {
    if (!confirm("Delete this flow?")) return;
    await delJSON(`/api/flows/${id}`).catch((e) => setErr((e as Error).message));
    if (selected === id) setSelected(null);
    load();
  }

  return (
    <div className="flows-mgr">
      {err && <div className="auto-msg err">{err}</div>}

      <div className="flows-layout">
        <aside className="flows-list">
          <div className="flows-new">
            <input
              className="auto-input"
              placeholder="New flow name"
              value={newName}
              onChange={(e) => setNewName(e.target.value)}
              onKeyDown={(e) => e.key === "Enter" && createFlow()}
            />
            <button type="button" className="auto-btn" onClick={createFlow}>
              +
            </button>
          </div>
          {flows.length === 0 ? (
            <p className="auto-empty">No flows yet. Name one to start.</p>
          ) : (
            <ul>
              {flows.map((f) => (
                <li key={f.id}>
                  <button
                    type="button"
                    className={`flows-list-item${selected === f.id ? " active" : ""}`}
                    onClick={() => setSelected(f.id)}
                  >
                    {f.name}
                    <span className="flows-list-count">{f.steps?.length ?? 0}</span>
                  </button>
                </li>
              ))}
            </ul>
          )}
        </aside>

        <section className="flows-detail">
          {current ? (
            <FlowDetail flow={current} actions={actions} onChange={load} onDelete={() => removeFlow(current.id)} />
          ) : (
            <p className="auto-empty">Pick a flow, or create one, to compose its steps.</p>
          )}
        </section>
      </div>
    </div>
  );
}

function FlowDetail({
  flow,
  actions,
  onChange,
  onDelete,
}: {
  flow: Flow;
  actions: Action[];
  onChange: () => void;
  onDelete: () => void;
}) {
  const [addActionId, setAddActionId] = useState<number | "">("");
  const [addKey, setAddKey] = useState("");
  const [addDeps, setAddDeps] = useState<string[]>([]);
  const [running, setRunning] = useState(false);
  const [result, setResult] = useState<RunResult | null>(null);
  const [err, setErr] = useState<string | null>(null);

  // Existing step keys are the candidates a new step can depend on.
  const existingKeys = flow.steps.map((s) => s.stepKey).filter(Boolean);

  async function addStep() {
    if (!addActionId || !addKey.trim()) {
      setErr("Pick an action and give the step a key.");
      return;
    }
    setErr(null);
    try {
      await postJSON(`/api/flows/${flow.id}/steps`, {
        actionId: addActionId,
        stepKey: addKey.trim(),
        position: flow.steps.length,
        dependsOn: addDeps,
      });
      setAddActionId("");
      setAddKey("");
      setAddDeps([]);
      onChange();
    } catch (e) {
      setErr(`Couldn't add step: ${(e as Error).message}`);
    }
  }

  async function run() {
    setRunning(true);
    setErr(null);
    setResult(null);
    try {
      const res = await postJSON<RunResult>(`/api/flows/${flow.id}:run`, {});
      setResult(res);
    } catch (e) {
      setErr(`Run failed: ${(e as Error).message}`);
    } finally {
      setRunning(false);
    }
  }

  const actionName = (id: number) => actions.find((a) => a.id === id)?.name || `action ${id}`;

  return (
    <div className="flow-detail">
      <div className="flow-detail-head">
        <h3>{flow.name}</h3>
        <div className="flow-detail-actions">
          <button type="button" className="auto-btn" disabled={running} onClick={run}>
            {running ? "Running…" : "▶ Run now"}
          </button>
          <button type="button" className="auto-btn link" onClick={onDelete}>
            Delete
          </button>
        </div>
      </div>

      {err && <div className="auto-msg err">{err}</div>}

      {/* Steps */}
      <ol className="flow-steps">
        {flow.steps.length === 0 && <li className="auto-empty">No steps yet — add the first below.</li>}
        {flow.steps.map((s) => (
          <li key={s.id} className="flow-step">
            <span className="flow-step-key">{s.stepKey}</span>
            <span className="flow-step-action">{actionName(s.actionId)}</span>
            {s.dependsOn && s.dependsOn.length > 0 && (
              <span className="flow-step-deps">after {s.dependsOn.join(", ")}</span>
            )}
          </li>
        ))}
      </ol>

      {/* Add-step form */}
      <div className="flow-add-step">
        <select className="auto-input" value={addActionId} onChange={(e) => setAddActionId(e.target.value ? Number(e.target.value) : "")}>
          <option value="">choose an action…</option>
          {actions.map((a) => (
            <option key={a.id} value={a.id}>
              {a.name} ({a.kind})
            </option>
          ))}
        </select>
        <input
          className="auto-input"
          placeholder="step key (e.g. pull)"
          value={addKey}
          onChange={(e) => setAddKey(e.target.value)}
        />
        {existingKeys.length > 0 && (
          <select
            className="auto-input"
            multiple
            value={addDeps}
            onChange={(e) => setAddDeps(Array.from(e.target.selectedOptions, (o) => o.value))}
            title="depends on (optional)"
          >
            {existingKeys.map((k) => (
              <option key={k} value={k}>
                after {k}
              </option>
            ))}
          </select>
        )}
        <button type="button" className="auto-btn" onClick={addStep}>
          + Add step
        </button>
      </div>

      <ScheduleEditor flowId={flow.id} />

      {/* Last run result */}
      {result && (
        <div className={`flow-result ${result.status === "ok" ? "ok" : "err"}`}>
          <div className="flow-result-head">
            run #{result.runId} — {result.status}
          </div>
          <ol>
            {result.steps.map((s, i) => (
              <li key={i} className={s.status === "ok" ? "ok" : "err"}>
                <b>{s.name}</b> — {s.status}
                {s.error ? <span className="flow-result-err"> · {s.error}</span> : null}
              </li>
            ))}
          </ol>
        </div>
      )}
    </div>
  );
}

function ScheduleEditor({ flowId }: { flowId: number }) {
  const [sched, setSched] = useState<Schedule>(null);
  const [cadence, setCadence] = useState<number>(CADENCES[2].seconds); // daily
  const [catchUp, setCatchUp] = useState(true);

  const load = useCallback(() => {
    getJSON<{ schedule: Schedule }>(`/api/flows/${flowId}/schedule`)
      .then((d) => {
        setSched(d.schedule);
        if (d.schedule) {
          setCadence(d.schedule.cadenceSeconds);
          setCatchUp(d.schedule.catchUp);
        }
      })
      .catch(() => {});
  }, [flowId]);
  useEffect(() => load(), [load]);

  async function save() {
    await postJSONPut(`/api/flows/${flowId}/schedule`, { cadenceSeconds: cadence, catchUp, enabled: true });
    load();
  }
  async function clear() {
    await delJSON(`/api/flows/${flowId}/schedule`);
    load();
  }

  return (
    <div className="flow-schedule">
      <span className="flow-schedule-label">🕕 Schedule</span>
      <select className="auto-input" value={cadence} onChange={(e) => setCadence(Number(e.target.value))}>
        {CADENCES.map((c) => (
          <option key={c.seconds} value={c.seconds}>
            {c.label}
          </option>
        ))}
      </select>
      <label className="flow-schedule-catchup" title="If the machine was off, fire once on wake rather than skipping.">
        <input type="checkbox" checked={catchUp} onChange={(e) => setCatchUp(e.target.checked)} /> catch up on wake
      </label>
      <button type="button" className="auto-btn" onClick={save}>
        {sched ? "Update" : "Set"}
      </button>
      {sched && (
        <button type="button" className="auto-btn link" onClick={clear}>
          remove
        </button>
      )}
      {sched?.lastRunAt && <span className="flow-schedule-last">last ran {sched.lastRunAt}</span>}
    </div>
  );
}

// The api client has no PUT helper; the schedule endpoint is PUT. Keep it local.
function postJSONPut(path: string, body: unknown): Promise<void> {
  return fetch(path, {
    method: "PUT",
    credentials: "same-origin",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  }).then((r) => {
    if (!r.ok) throw new Error(`HTTP ${r.status}`);
  });
}
