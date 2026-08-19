import { useCallback, useEffect, useRef, useState } from "react";
import { getJSON, delJSON } from "../api/client";
import { ChatPanel } from "./ChatPanel";

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
  createdAt: string;
};

// A job is "live" (still doing or able to do work) in these states.
const LIVE = new Set(["preparing", "running", "idle"]);

// statusDot maps a job status to a small colored indicator class.
function statusClass(status: string): string {
  if (status === "running" || status === "preparing") return "work-dot running";
  if (status === "idle") return "work-dot idle";
  if (status === "failed" || status === "canceled" || status === "interrupted") return "work-dot err";
  return "work-dot done";
}

export function WorkTab({ onCount }: { onCount?: (n: number) => void }) {
  const [jobs, setJobs] = useState<MergeJob[]>([]);
  const [active, setActive] = useState<string | null>(null);
  const loadedOnce = useRef(false);

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
      const ok = window.confirm(
        `"${job.repoName} #${job.prNumber}" is still merging. Stop it and remove the job?`,
      );
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

  if (loadedOnce.current && jobs.length === 0) {
    return (
      <div className="work-empty">
        No merge jobs. Start one with <b>Merge with host</b> on a PR — it runs here in the
        background, so you can navigate away and come back.
      </div>
    );
  }

  const activeJob = jobs.find((j) => j.id === active) || null;

  return (
    <div className="work-tab">
      <div className="work-rail">
        {jobs.map((j) => (
          <div key={j.id} className={`work-rail-item${active === j.id ? " active" : ""}`}>
            <button type="button" className="work-rail-btn" onClick={() => setActive(j.id)} title={`${j.status} · ${j.strategy}`}>
              <i className={statusClass(j.status)} />
              <span className="work-rail-label">
                {j.repoName} <span className="work-rail-pr">#{j.prNumber}</span>
              </span>
              <span className="work-rail-status">{j.status}</span>
            </button>
            <button type="button" className="work-rail-close" title="Close job (ends it if running)" onClick={() => close(j)}>
              ✕
            </button>
          </div>
        ))}
      </div>

      <div className="work-view">
        {activeJob ? (
          <>
            <div className="work-view-head">
              <span className="ai-warn" title="Runs your host machine's Claude with Bash against a real git checkout; not sandboxed">
                host · not sandboxed
              </span>
              <span className="work-view-title">
                {activeJob.repoName} #{activeJob.prNumber} · {activeJob.strategy}
              </span>
            </div>
            <div className="work-view-hint">
              This runs the editable <b>pr.merge</b> prompt — change it in{" "}
              <a href="/automations">Automations → Prompts</a>.
            </div>
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
