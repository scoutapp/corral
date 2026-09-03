import { useCallback, useEffect, useRef, useState } from "react";
import { getJSON, delJSON, postJSON } from "../api/client";
import { ChatPanel } from "./ChatPanel";
import { useDragResize } from "../hooks/useDragResize";
import { usePersistentState } from "../hooks/usePersistentState";

// The jobs rail is user-draggable (handle on its RIGHT edge) and persisted, so
// worker/merge labels aren't crammed into a thin column.
const RAIL_W_KEY = "corral.workRailWidth";
const RAIL_W_DEFAULT = 150;

// WorkTab is the ChatDock's "Work" surface: the list of host-merge background
// jobs (which run detached on the server), with a left rail to switch between
// them and a live viewer for the selected one. Jobs keep running when you close
// the dock or navigate away; closing a job here ends it (DELETE) after a confirm
// if it's still running.

type MergeJob = {
  id: string;
  prId: number;
  repoId: string;
  prNumber: number;
  repoName: string;
  strategy: string;
  status: string; // preparing | running | idle | done | failed | canceled | interrupted
  activity?: string; // "working" | "idle" | "" — live output-recency signal (server-side)
  kind?: string; // "merge" | "worker"
  title?: string; // worker label (worker kind)
  createdAt: string;
};

// A job is "live" (still doing or able to do work) in these states.
const LIVE = new Set(["preparing", "running", "idle"]);

// jobLabel is the human name for a job: a worker's title, else "repo #PR".
function jobLabel(j: { kind?: string; title?: string; repoName: string; prNumber: number }): string {
  if (j.kind === "worker") return j.title || "worker";
  return `${j.repoName} #${j.prNumber}`;
}

// statusClass maps a job to its indicator dot. For a LIVE job the server-side
// activity signal drives it (recent output = working/green pulse, quiet = idle/
// dim). Terminal statuses keep their own color.
function statusClass(job: { status: string; activity?: string }): string {
  if (LIVE.has(job.status)) {
    return job.activity === "idle" ? "work-dot idle" : "work-dot running";
  }
  if (job.status === "failed" || job.status === "canceled" || job.status === "interrupted") return "work-dot err";
  return "work-dot done";
}

export function WorkTab({ onCount }: { onCount?: (n: number) => void }) {
  const [jobs, setJobs] = useState<MergeJob[]>([]);
  const [active, setActive] = useState<string | null>(null);
  const loadedOnce = useRef(false);
  // "New task" composer: spawn a fresh worker Claude with a title + prompt.
  const [composing, setComposing] = useState(false);
  const [taskTitle, setTaskTitle] = useState("");
  const [taskPrompt, setTaskPrompt] = useState("");
  const [spawning, setSpawning] = useState(false);
  const [railWidth, setRailWidth] = usePersistentState<number>(RAIL_W_KEY, RAIL_W_DEFAULT);
  const railResizeRef = useDragResize({
    axis: "x",
    edge: "end", // handle on the right edge; dragging right grows the rail
    get: () => railWidth,
    min: 110,
    max: () => Math.round(window.innerWidth * 0.5),
    onResize: setRailWidth,
  });

  const refresh = useCallback(() => {
    getJSON<{ jobs: MergeJob[] }>("/merge-jobs")
      .then((d) => {
        const list = d.jobs || [];
        setJobs(list);
        onCount?.(list.length);
        // Default-select the newest job the first time we see any.
        setActive((cur) => {
          if (cur && list.some((j) => j.id === cur)) return cur;
          return list.length ? list[0].id : null;
        });
        loadedOnce.current = true;
      })
      .catch(() => {});
  }, [onCount]);

  // Poll the job list: often while any job is live (status changes), lazily
  // otherwise (a finished list rarely changes).
  useEffect(() => {
    refresh();
    const anyLive = jobs.some((j) => LIVE.has(j.status));
    const ms = anyLive ? 2500 : 8000;
    const t = setInterval(refresh, ms);
    return () => clearInterval(t);
  }, [refresh, jobs]);

  const close = async (job: MergeJob) => {
    if (LIVE.has(job.status)) {
      const verb = job.kind === "worker" ? "still running" : "still merging";
      const ok = window.confirm(`"${jobLabel(job)}" is ${verb}. Stop it and remove the job?`);
      if (!ok) return;
    }
    try {
      await delJSON(`/merge-jobs/${encodeURIComponent(job.id)}`);
    } catch {
      /* ignore; the poll will reconcile */
    }
    setActive((cur) => (cur === job.id ? null : cur));
    refresh();
  };

  const spawnWorker = async () => {
    if (!taskPrompt.trim()) return;
    setSpawning(true);
    try {
      const r = await postJSON<{ jobId: string }>("/api/conductor/workers", {
        title: taskTitle.trim() || undefined,
        prompt: taskPrompt,
      });
      setComposing(false);
      setTaskTitle("");
      setTaskPrompt("");
      setActive(r.jobId); // focus the new worker
      refresh();
    } catch {
      /* ignore; surfaced by the next poll if it started */
    } finally {
      setSpawning(false);
    }
  };

  const activeJob = jobs.find((j) => j.id === active) || null;

  // The "New task" composer, shared by the empty-state and the rail header.
  const composer = (
    <div className="work-compose">
      <input
        className="work-compose-title"
        type="text"
        placeholder="Task title (optional)"
        value={taskTitle}
        onChange={(e) => setTaskTitle(e.target.value)}
      />
      <textarea
        className="work-compose-prompt"
        rows={4}
        placeholder="What should this worker Claude do? It runs fresh and independent, in the background."
        value={taskPrompt}
        onChange={(e) => setTaskPrompt(e.target.value)}
      />
      <div className="work-compose-actions">
        <button type="button" className="btn primary" disabled={spawning || !taskPrompt.trim()} onClick={spawnWorker}>
          {spawning ? "Starting…" : "Start worker"}
        </button>
        <button type="button" className="btn" onClick={() => setComposing(false)}>
          Cancel
        </button>
      </div>
      <div className="work-compose-note ai-warn">host · not sandboxed · uses your global chat capability</div>
    </div>
  );

  if (loadedOnce.current && jobs.length === 0) {
    return (
      <div className="work-tab-empty">
        {composing ? (
          composer
        ) : (
          <>
            <div className="work-empty">
              No background jobs yet. Delegate a task to a fresh worker Claude, or start a{" "}
              <b>Merge with host</b> on a PR — both run here so you can navigate away and come back.
            </div>
            <button type="button" className="btn primary work-new-btn" onClick={() => setComposing(true)}>
              + New task
            </button>
          </>
        )}
      </div>
    );
  }

  return (
    <div className="work-tab">
      <div className="work-rail" style={{ flex: `0 0 ${railWidth}px` }}>
        <div className="work-rail-head">
          <span>Jobs</span>
          <button type="button" className="work-rail-new" title="Delegate a task to a new worker Claude" onClick={() => setComposing((v) => !v)}>
            + New
          </button>
        </div>
        {composing && <div className="work-rail-compose">{composer}</div>}
        {jobs.map((j) => (
          <div key={j.id} className={`work-rail-item${active === j.id ? " active" : ""}`}>
            <button type="button" className="work-rail-btn" onClick={() => setActive(j.id)} title={`${j.status} · ${j.strategy}`}>
              <i className={statusClass(j)} />
              <span className="work-rail-label">
                {j.kind === "worker" ? (
                  j.title || "worker"
                ) : (
                  <>
                    {j.repoName} <span className="work-rail-pr">#{j.prNumber}</span>
                  </>
                )}
              </span>
              <span className="work-rail-status">{LIVE.has(j.status) && j.activity ? j.activity : j.status}</span>
            </button>
            <button type="button" className="work-rail-close" title="Close job (ends it if running)" onClick={() => close(j)}>
              ✕
            </button>
          </div>
        ))}
      </div>

      <div className="work-rail-resize" ref={railResizeRef} title="Drag to resize the list" />

      <div className="work-view">
        {activeJob ? (
          <>
            <div className="work-view-head">
              <span className="ai-warn" title="Runs your host machine's Claude; not sandboxed">
                host · not sandboxed
              </span>
              <span className="work-view-title">
                {jobLabel(activeJob)}
                {activeJob.kind !== "worker" && activeJob.strategy ? ` · ${activeJob.strategy}` : ""}
              </span>
            </div>
            {activeJob.kind !== "worker" && (
              <div className="work-view-hint">
                This runs the editable <b>pr.merge</b> prompt — change it in{" "}
                <a href="/automations">Automations → Prompts</a>.
              </div>
            )}
            {/* Re-mount the panel per job id so switching jobs points the WS at the
                right one and replays that job's transcript. */}
            <div className="work-view-panel">
              <ChatPanel
                key={activeJob.id}
                wsPath={`/merge-jobs/${encodeURIComponent(activeJob.id)}/ws`}
                canAct
                persistKey={`merge-job-${activeJob.id}`}
              />
            </div>
          </>
        ) : (
          <div className="work-empty">Select a job to view its progress.</div>
        )}
      </div>
    </div>
  );
}
