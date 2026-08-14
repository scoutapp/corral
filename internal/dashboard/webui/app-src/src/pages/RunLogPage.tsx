import { useCallback, useEffect, useState } from "react";
import { Link } from "../router";
import { getJSON } from "../api/client";
import { useBodyClass } from "../hooks/useBodyClass";

// Run history: the append-only record of every automation execution. This is
// what makes best-effort secondary failures visible — a hook that failed shows
// here even though it didn't block the primary action. Backed by /api/runs.

type Step = {
  actionId: number;
  kind: string;
  name: string;
  status: string;
  output?: string;
  error?: string;
  durationMs: number;
};
type Run = {
  id: number;
  trigger: string;
  event?: string;
  targetKind: string;
  targetId: number;
  status: string;
  context: string; // raw JSON
  steps: string; // raw JSON
  startedAt: string;
  finishedAt?: string;
};

function statusClass(s: string): string {
  switch (s) {
    case "ok":
      return "run-ok";
    case "error":
      return "run-err";
    case "partial":
      return "run-partial";
    default:
      return "run-running";
  }
}

function parseSteps(raw: string): Step[] {
  try {
    return JSON.parse(raw) || [];
  } catch {
    return [];
  }
}

export function RunLogPage() {
  useBodyClass("console");
  const [runs, setRuns] = useState<Run[]>([]);
  const [open, setOpen] = useState<Record<number, boolean>>({});
  const [err, setErr] = useState<string | null>(null);

  const load = useCallback(() => {
    getJSON<{ runs: Run[] }>("/api/runs?limit=100")
      .then((d) => setRuns(d.runs || []))
      .catch((e) => setErr((e as Error).message));
  }, []);

  useEffect(() => {
    load();
  }, [load]);

  return (
    <>
      <header className="console-header">
        <div className="brand">
          <Link to="/automations" className="back">
            ← Automations
          </Link>
          <span className="brand-name">Run history</span>
          <button type="button" className="brand-sub auto-btn link" onClick={load}>
            ⟳ refresh
          </button>
        </div>
      </header>

      <div className="auto-page">
        {err && <div className="auto-msg err">{err}</div>}
        {runs.length === 0 ? (
          <p className="auto-empty">No runs yet. Actions and hooks record their executions here.</p>
        ) : (
          <ul className="run-list">
            {runs.map((run) => {
              const steps = parseSteps(run.steps);
              const isOpen = !!open[run.id];
              return (
                <li key={run.id} className="run-item">
                  <button
                    type="button"
                    className="run-head"
                    onClick={() => setOpen((o) => ({ ...o, [run.id]: !o[run.id] }))}
                  >
                    <span className={`run-badge ${statusClass(run.status)}`}>{run.status}</span>
                    <span className="run-what">
                      {run.event ? <code>{run.event}</code> : run.trigger}
                      <span className="run-meta">
                        {run.trigger} · {steps.length} step{steps.length === 1 ? "" : "s"}
                      </span>
                    </span>
                    <span className="run-time">{run.startedAt}</span>
                    <span className="run-caret">{isOpen ? "▾" : "▸"}</span>
                  </button>
                  {isOpen && (
                    <div className="run-steps">
                      {steps.length === 0 && <div className="run-empty">no steps recorded</div>}
                      {steps.map((s, i) => (
                        <div key={i} className="run-step">
                          <div className="run-step-head">
                            <span className={`run-dot ${statusClass(s.status)}`} />
                            <b>{s.name || `#${s.actionId}`}</b>
                            <code className="run-step-kind">{s.kind}</code>
                            <span className="run-step-dur">{s.durationMs}ms</span>
                          </div>
                          {s.output && <pre className="run-out">{s.output}</pre>}
                          {s.error && <pre className="run-out run-out-err">{s.error}</pre>}
                        </div>
                      ))}
                      {run.context && run.context !== "{}" && (
                        <details className="run-ctx">
                          <summary>context</summary>
                          <pre className="run-out">{run.context}</pre>
                        </details>
                      )}
                    </div>
                  )}
                </li>
              );
            })}
          </ul>
        )}
      </div>
    </>
  );
}
