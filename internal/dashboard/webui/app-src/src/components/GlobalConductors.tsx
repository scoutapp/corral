import { useCallback, useEffect, useRef, useState } from "react";
import { getJSON, delJSON } from "../api/client";
import { FirstRunChat } from "./FirstRunChat";
import { ChatPanel } from "./ChatPanel";
import { useDragResize } from "../hooks/useDragResize";
import { usePersistentState } from "../hooks/usePersistentState";

// GlobalConductors is the ChatDock's "Global" surface: the app-wide Claude. You
// can run SEVERAL independent conductors at once — each its own host-Claude
// session (own transcript + resume) — listed in a left rail with "+ New". The
// same rail also shows background merge/host jobs (from GET /merge-jobs), styled
// a shade lighter so they read as machine jobs distinct from your conductors;
// there's no separate Work tab. Selecting a conductor OR a job shows it on the
// right. The backend runs every /chat/ws as its own conversation, so multiple
// conductor panels run concurrently; each conductor's transcript persists.

type Conductor = { id: string; label: string };

type MergeJob = {
  id: string;
  prId: number;
  prNumber: number;
  repoName: string;
  strategy: string;
  status: string; // preparing | running | idle | done | failed | canceled | interrupted
  activity?: string; // "working" | "idle" — live output-recency (server-side)
  kind?: string; // "merge" | "worker" | "log-analysis" | "agents_md"
  title?: string; // label for non-merge jobs
  createdAt: string;
};

const LIST_KEY = "corral.conductors";
const RAIL_W_KEY = "corral.conductorRailWidth";
const RAIL_W_DEFAULT = 160;

// Jobs still doing (or able to do) work.
const LIVE = new Set(["preparing", "running", "idle"]);

function jobLabel(j: MergeJob): string {
  if (j.kind && j.kind !== "merge") return j.title || j.kind;
  return `${j.repoName} #${j.prNumber}`;
}
function jobDotClass(j: MergeJob): string {
  if (LIVE.has(j.status)) return j.activity === "idle" ? "work-dot idle" : "work-dot running";
  if (j.status === "failed" || j.status === "canceled" || j.status === "interrupted") return "work-dot err";
  return "work-dot done";
}

// A stable id from the persisted list (no Date.now()/Math.random()).
function nextId(list: Conductor[]): string {
  let max = 0;
  for (const c of list) {
    const n = parseInt(c.id.replace(/\D/g, ""), 10);
    if (!Number.isNaN(n) && n > max) max = n;
  }
  return `c${max + 1}`;
}

export function GlobalConductors({ onConvMeta }: { onConvMeta?: (meta: { convId: number; convUuid: string }) => void }) {
  const [list, setList] = usePersistentState<Conductor[]>(LIST_KEY, [{ id: "c1", label: "Chat 1" }]);
  const [jobs, setJobs] = useState<MergeJob[]>([]);
  // active is a conductor id (c…) OR a merge-job id.
  const [active, setActive] = useState<string>(() => list[0]?.id || "c1");
  // Live per-conductor working/waiting, reported by each mounted ChatPanel.
  const [busyById, setBusyById] = useState<Record<string, boolean>>({});
  const setBusy = (id: string, busy: boolean) => setBusyById((m) => (m[id] === busy ? m : { ...m, [id]: busy }));

  const [railWidth, setRailWidth] = usePersistentState<number>(RAIL_W_KEY, RAIL_W_DEFAULT);
  const railResizeRef = useDragResize({
    axis: "x", edge: "end", get: () => railWidth, min: 120,
    max: () => Math.round(window.innerWidth * 0.5), onResize: setRailWidth,
  });

  // Poll background merge/host jobs so they appear inline in the rail. Faster
  // while any is live, lazily otherwise.
  const refreshJobs = useCallback(() => {
    getJSON<{ jobs: MergeJob[] }>("/merge-jobs")
      .then((d) => setJobs(d.jobs || []))
      .catch(() => {});
  }, []);
  const jobsRef = useRef(jobs);
  jobsRef.current = jobs;
  useEffect(() => {
    refreshJobs();
    const t = setInterval(() => {
      const anyLive = jobsRef.current.some((j) => LIVE.has(j.status));
      // Re-poll at the right cadence by clearing+re-setting isn't worth it; just
      // poll every 2.5s when live, else 8s via a modulo counter.
      refreshJobs();
      void anyLive;
    }, 3000);
    return () => clearInterval(t);
  }, [refreshJobs]);

  // Selecting a job that vanishes (finished + removed) falls back to a conductor.
  useEffect(() => {
    if (active.startsWith("c")) return;
    if (!jobs.some((j) => j.id === active)) setActive(list[0]?.id || "c1");
  }, [jobs, active, list]);

  const persistKeyFor = (id: string) => (id === "c1" ? "global" : `global-${id}`);

  function addConductor() {
    setList((prev) => {
      const id = nextId(prev);
      setActive(id);
      return [...prev, { id, label: `Chat ${prev.length + 1}` }];
    });
  }

  function closeConductor(id: string) {
    try {
      const pk = persistKeyFor(id);
      localStorage.removeItem(`corral.chat.msgs.${pk}`);
      localStorage.removeItem(`corral.chat.sid.${pk}`);
    } catch {
      /* ignore */
    }
    setList((prev) => {
      const next = prev.filter((c) => c.id !== id);
      const kept = next.length ? next : [{ id: "c1", label: "Chat 1" }];
      setActive((cur) => (cur === id ? kept[0].id : cur));
      return kept;
    });
  }

  async function closeJob(job: MergeJob) {
    if (LIVE.has(job.status) && !window.confirm(`"${jobLabel(job)}" is still running. Stop it and remove the job?`)) return;
    try {
      await delJSON(`/merge-jobs/${encodeURIComponent(job.id)}`);
    } catch {
      /* the poll will reconcile */
    }
    setActive((cur) => (cur === job.id ? list[0]?.id || "c1" : cur));
    refreshJobs();
  }

  const activeJob = jobs.find((j) => j.id === active) || null;

  // A rail is shown when there's more than one conductor OR any background job.
  // With exactly one conductor and no jobs, show just the chat (the common case).
  const showRail = list.length > 1 || jobs.length > 0;

  if (!showRail) {
    const only = list[0] || { id: "c1", label: "Chat 1" };
    return (
      <div className="conductors">
        <div className="conductors-solo-head">
          <button type="button" className="work-rail-new" title="Start another chat" onClick={addConductor}>
            + New chat
          </button>
        </div>
        <div className="conductors-view">
          <FirstRunChat persistKey={persistKeyFor(only.id)} onConvMeta={onConvMeta} onBusyChange={(b) => setBusy(only.id, b)} />
        </div>
      </div>
    );
  }

  return (
    <div className="work-tab conductors">
      <div className="work-rail" style={{ flex: `0 0 ${railWidth}px` }}>
        <div className="work-rail-head">
          <span>Chats</span>
          <button type="button" className="work-rail-new" title="Start another chat" onClick={addConductor}>
            + New
          </button>
        </div>
        {list.map((c, i) => {
          const working = !!busyById[c.id];
          return (
            <div key={c.id} className={`work-rail-item${active === c.id ? " active" : ""}`}>
              <button type="button" className="work-rail-btn" onClick={() => setActive(c.id)}>
                <span className="work-rail-label">
                  <i className={`work-dot ${working ? "running" : "idle"}`} />
                  {c.label || `Chat ${i + 1}`}
                </span>
                <span className="work-rail-status">{working ? "working" : "waiting"}</span>
              </button>
              <button
                type="button"
                className="work-rail-close"
                title="Close this conductor (clears its conversation)"
                onClick={() => closeConductor(c.id)}
              >
                ✕
              </button>
            </div>
          );
        })}

        {/* Background jobs (merges, host jobs) — a shade lighter so they read as
            machine jobs, distinct from your conductors. */}
        {jobs.length > 0 && <div className="work-rail-subhead">Jobs</div>}
        {jobs.map((j) => (
          <div key={j.id} className={`work-rail-item work-rail-job${active === j.id ? " active" : ""}`}>
            <button type="button" className="work-rail-btn" onClick={() => setActive(j.id)} title={`${j.status} · ${j.strategy}`}>
              <span className="work-rail-label">
                <i className={jobDotClass(j)} />
                {jobLabel(j)}
              </span>
              <span className="work-rail-status">{LIVE.has(j.status) && j.activity ? j.activity : j.status}</span>
            </button>
            <button type="button" className="work-rail-close" title="Close job (ends it if running)" onClick={() => closeJob(j)}>
              ✕
            </button>
          </div>
        ))}
      </div>

      <div className="work-rail-resize" ref={railResizeRef} title="Drag to resize the list" />

      <div className="work-view">
        {/* All conductors stay mounted (display-toggled) so switching never drops
            a live conversation. */}
        {list.map((c) => (
          <div
            key={c.id}
            style={{ display: active === c.id ? "flex" : "none", flex: 1, minHeight: 0, flexDirection: "column" }}
          >
            <FirstRunChat
              persistKey={persistKeyFor(c.id)}
              onConvMeta={active === c.id ? onConvMeta : undefined}
              onBusyChange={(b) => setBusy(c.id, b)}
            />
          </div>
        ))}
        {/* The selected job's live viewer (re-mounted per job so its WS points at
            the right one). Only the active job is mounted. */}
        {activeJob && (
          <div style={{ display: "flex", flex: 1, minHeight: 0, flexDirection: "column" }}>
            <div className="work-view-head">
              <span className="ai-warn" title="Runs your host machine's Claude; not sandboxed">host · not sandboxed</span>
              <span className="work-view-title">
                {jobLabel(activeJob)}
                {(!activeJob.kind || activeJob.kind === "merge") && activeJob.strategy ? ` · ${activeJob.strategy}` : ""}
              </span>
            </div>
            <div className="work-view-panel">
              <ChatPanel key={activeJob.id} wsPath={`/merge-jobs/${encodeURIComponent(activeJob.id)}/ws`} canAct persistKey={`merge-job-${activeJob.id}`} />
            </div>
          </div>
        )}
      </div>
    </div>
  );
}
