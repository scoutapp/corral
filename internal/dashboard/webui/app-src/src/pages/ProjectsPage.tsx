import { useEffect, useRef, useState } from "react";
import { Link } from "../router";
import { useStatus } from "../hooks/useStatus";
import { useMutes } from "../hooks/useMutes";
import { chime } from "../lib/chime";
import { useToasts } from "../components/Toasts";
import { SSHLoadModal } from "../components/SSHLoadModal";
import { postRaw } from "../api/client";
import type { StatusRow } from "../api/types";
import { ReposSection } from "./ReposSection";
import { PRInboxSection } from "./PRInboxSection";
import { NewProjectModal, AddRepoModal } from "./ReposModals";
import { useBodyClass } from "../hooks/useBodyClass";

// Landing page: live "panes into work". Polls /status, renders one pane per
// project sorted by urgency (waiting-on-you, then working, then idle), chimes on
// working->waiting for unmuted projects, and offers per-pane power/mute/remove.
// Port of panes.js + the index.html.tmpl shell.

const RANK: Record<string, number> = { waiting: 0, working: 1, off: 2 };

function activityLabel(a: string): string {
  if (a === "working") return "working";
  if (a === "waiting") return "waiting on you";
  return "idle";
}

function summarize(projects: StatusRow[]): string {
  let w = 0,
    q = 0,
    off = 0;
  for (const p of projects) {
    if (p.activity === "working") w++;
    else if (p.activity === "waiting") q++;
    else off++;
  }
  const parts: string[] = [];
  if (q) parts.push(`${q} waiting on you`);
  if (w) parts.push(`${w} working`);
  if (off) parts.push(`${off} idle`);
  return `${projects.length} project${projects.length === 1 ? "" : "s"}${parts.length ? " — " + parts.join(", ") : ""}`;
}

function Dot({ up }: { up: boolean }) {
  return <i className={`pdot ${up ? "on" : "off"}`} />;
}

export function ProjectsPage() {
  useBodyClass("console");
  const { projects, bootId, connected, loaded } = useStatus(3000);
  const { isMuted, mutedAll, toggleMute, toggleMuteAll, forgetMute } = useMutes(bootId);
  const { notify } = useToasts();

  const [summary, setSummary] = useState("connecting…");
  const [summaryAttn, setSummaryAttn] = useState(false);
  const [busy, setBusy] = useState<Record<string, boolean>>({});
  const [sshFor, setSshFor] = useState<{ id: string; name: string } | null>(null);

  // Sections + landing-page modals (Repos, New project, Add repo).
  const [section, setSection] = useState<"projects" | "repos" | "prs">("projects");
  const [reposSearch, setReposSearch] = useState("");
  const [reposReload, setReposReload] = useState(0);
  const [newProject, setNewProject] = useState(false);
  const [addRepo, setAddRepo] = useState(false);

  // Edge-detection memory for the chime + toasts (seeded on first poll so we
  // don't fire for projects already waiting at load).
  const lastActivity = useRef<Record<string, string>>({});
  const seeded = useRef(false);

  useEffect(() => {
    // Until the first poll resolves, keep the initial "connecting…" — don't flash
    // "lost connection" (connected is false only because nothing has returned yet).
    if (!loaded) return;
    if (!connected) {
      setSummary("lost connection");
      setSummaryAttn(true);
      return;
    }
    // Detect working -> waiting edges: chime (if unmuted) + cross-project toast.
    let play = false;
    for (const p of projects) {
      const prev = lastActivity.current[p.id];
      const act = p.activity || "off";
      if (seeded.current && prev === "working" && act === "waiting") {
        if (!isMuted(p.id)) play = true;
        notify(p.id, p.name);
      }
      lastActivity.current[p.id] = act;
    }
    if (play) chime();
    seeded.current = true;

    setSummary(summarize(projects));
    setSummaryAttn(projects.some((p) => p.activity === "waiting"));
  }, [projects, connected, loaded, isMuted, notify]);

  const sorted = [...projects].sort((a, b) => {
    const ra = RANK[a.activity] ?? 3;
    const rb = RANK[b.activity] ?? 3;
    if (ra !== rb) return ra - rb;
    return a.name.localeCompare(b.name);
  });

  async function power(p: StatusRow) {
    const stopping = p.container_up;
    setBusy((b) => ({ ...b, [p.id]: true }));
    try {
      const res = await postRaw(`/p/${encodeURIComponent(p.id)}/${stopping ? "stop" : "start"}`);
      const body = await res.json().catch(() => ({}));
      if (!stopping && res.status === 409 && body?.ssh_keys_pending) {
        setSummary(`"${p.name}" needs SSH keys — load them to start.`);
        setSummaryAttn(true);
        setBusy((b) => ({ ...b, [p.id]: false }));
        setSshFor({ id: p.id, name: p.name });
        return;
      }
      if (res.status >= 400) throw new Error(body?.message || `HTTP ${res.status}`);
      setSummary(`${stopping ? "stopping " : "starting "}${p.name}…`);
      setSummaryAttn(false);
    } catch (err) {
      setSummary(`${stopping ? "stop" : "start"} failed: ${(err as Error).message}`);
      setSummaryAttn(true);
    } finally {
      setBusy((b) => ({ ...b, [p.id]: false }));
    }
  }

  async function startById(id: string, name: string) {
    try {
      const res = await postRaw(`/p/${encodeURIComponent(id)}/start`);
      const body = await res.json().catch(() => ({}));
      if (res.status >= 400) throw new Error(body?.message || `HTTP ${res.status}`);
      setSummary(`starting ${name}…`);
      setSummaryAttn(false);
    } catch (err) {
      setSummary(`start failed: ${(err as Error).message}`);
      setSummaryAttn(true);
    }
  }

  async function remove(p: StatusRow) {
    if (
      !window.confirm(
        `Remove "${p.name}" from the dashboard?\n\nThis only unregisters it here — its .corral/ config and logs are kept, and it reappears if you start it again.`,
      )
    )
      return;
    try {
      const res = await postRaw(`/p/${encodeURIComponent(p.id)}/remove`);
      if (!res.ok) throw new Error(`HTTP ${res.status}`);
      forgetMute(p.id);
    } catch (err) {
      setSummary(`remove failed: ${(err as Error).message}`);
      setSummaryAttn(true);
    }
  }

  return (
    <>
    <div className="app-shell">
      <nav className="sidebar">
        <div className="brand">
          <span className="brand-mark">◇</span>
          <span className="brand-name">corral</span>
        </div>
        <button type="button" className={`nav-item${section === "projects" ? " active" : ""}`} onClick={() => setSection("projects")}>
          <span className="nav-ico">▤</span> Projects
        </button>
        <button type="button" className={`nav-item${section === "repos" ? " active" : ""}`} onClick={() => setSection("repos")}>
          <span className="nav-ico">◧</span> Repos
        </button>
        <button type="button" className={`nav-item${section === "prs" ? " active" : ""}`} onClick={() => setSection("prs")}>
          <span className="nav-ico">⑃</span> PRs
        </button>

        {/* Automations is its own thing — set apart from the browse sections
            above and the project/settings controls below. */}
        <div className="nav-sep" />
        <Link to="/automations" className="nav-item nav-link nav-automations">
          <span className="nav-ico">⚡</span> Automations
        </Link>

        <div className="sidebar-spacer" />
        <button type="button" className="sidebar-cta" title="Create a new project" onClick={() => setNewProject(true)}>
          + New project
        </button>
        <Link to="/global" className="nav-item nav-link">
          ⚙ Global settings
        </Link>
      </nav>

      <div className="content">
        <header className="content-head">
          <h1 id="section-title">
            {section === "repos" ? "Repos" : section === "prs" ? "PRs" : "Projects"}
          </h1>
          <div className="head-right">
            <div className="legend">
              <span className="legend-item">
                <i className="dot working" />
                working
              </span>
              <span className="legend-item">
                <i className="dot waiting" />
                waiting
              </span>
              <span className="legend-item">
                <i className="dot off" />
                idle
              </span>
            </div>
            <button
              type="button"
              className="btn mute-all"
              aria-pressed={mutedAll}
              title={mutedAll ? "Unmute all alerts" : "Mute all alerts"}
              onClick={toggleMuteAll}
            >
              {mutedAll ? "🔇 muted" : "🔔 alerts"}
            </button>
          </div>
        </header>

        {section === "repos" && (
          <section className="section active">
            <div className="section-toolbar">
              <input
                className="section-search"
                type="search"
                placeholder="Filter repos…"
                autoComplete="off"
                spellCheck={false}
                value={reposSearch}
                onChange={(e) => setReposSearch(e.target.value)}
              />
              <button type="button" className="btn primary" title="Add a repository" onClick={() => setAddRepo(true)}>
                + Add repo
              </button>
            </div>
            <ReposSection search={reposSearch} reloadKey={reposReload} />
          </section>
        )}

        {section === "prs" && (
          <section className="section active">
            <PRInboxSection />
          </section>
        )}

        <section className="section active" style={{ display: section === "projects" ? "" : "none" }}>
          <main className="panes">
            {/* Before the first /status resolves, show skeleton panes rather than
                the empty state — otherwise "No projects yet" flashes then vanishes
                (FOUC) as soon as the poll returns real projects. */}
            {!loaded &&
              Array.from({ length: 3 }).map((_, i) => (
                <div key={`sk-${i}`} className="pane pane-skeleton" aria-hidden="true">
                  <div className="pane-top">
                    <span className="pane-name skel skel-line" style={{ width: "40%" }} />
                    <span className="skel skel-line" style={{ width: "5rem" }} />
                  </div>
                  <div className="pane-peek">
                    <span className="skel skel-line" style={{ width: "70%" }} />
                  </div>
                  <div className="pane-foot">
                    <span className="skel skel-line" style={{ width: "3rem" }} />
                    <span className="skel skel-line" style={{ width: "3rem" }} />
                    <span className="skel skel-line" style={{ width: "3rem" }} />
                  </div>
                </div>
              ))}
            {loaded && sorted.length === 0 && (
              <p className="empty">
                No projects yet. Start one with <code>corral init</code> then <code>corral start</code>.
              </p>
            )}
            {sorted.map((p) => {
              const act = p.activity || "off";
              const peek = p.peek || (act === "off" ? "container not running" : "…");
              const m = isMuted(p.id);
              return (
                <Link key={p.id} className={`pane pane-${act}`} to={`/p/${p.id}`}>
                  <div className="pane-top">
                    <span className="pane-name">{p.name}</span>
                    <span className={`pane-state state-${act}`}>
                      <i className="beacon" />
                      {activityLabel(act)}
                      <button
                        className={`power-btn ${p.container_up ? "stop" : "start"}`}
                        type="button"
                        disabled={busy[p.id]}
                        title={p.container_up ? "Stop this project's container" : "Start this project's container"}
                        onClick={(e) => {
                          e.preventDefault();
                          e.stopPropagation();
                          power(p);
                        }}
                      >
                        {busy[p.id] ? <span className="power-spin">↻</span> : p.container_up ? "■" : "▶"}
                      </button>
                      <button
                        className="mute-btn"
                        type="button"
                        aria-pressed={m}
                        title={m ? "Unmute alerts for this project" : "Mute alerts for this project"}
                        onClick={(e) => {
                          e.preventDefault();
                          e.stopPropagation();
                          toggleMute(p.id);
                        }}
                      >
                        {m ? "🔇" : "🔔"}
                      </button>
                      {act === "off" && (
                        <button
                          className="remove-btn"
                          type="button"
                          title="Remove this idle project from the dashboard"
                          onClick={(e) => {
                            e.preventDefault();
                            e.stopPropagation();
                            remove(p);
                          }}
                        >
                          ✕
                        </button>
                      )}
                    </span>
                  </div>
                  <div className="pane-peek">
                    <span className="peek-caret">&gt;</span> {peek}
                  </div>
                  <div className="pane-foot">
                    <span className="svc">
                      <Dot up={p.container_up} />box
                    </span>
                    <span className="svc">
                      <Dot up={p.mitm_up} />proxy
                    </span>
                    <span className="svc">
                      <Dot up={p.tmux_up} />session
                    </span>
                    {act === "working" && <span className="rate">{p.anthropic_hits} req/min · live</span>}
                    {act === "waiting" && <span className="rate quiet">idle at prompt</span>}
                  </div>
                </Link>
              );
            })}
          </main>
        </section>
      </div>
    </div>

    <footer className="console-footer">
      <span className={summaryAttn ? "attention" : "muted"}>{summary}</span>
    </footer>

    {sshFor && (
        <SSHLoadModal
          projectId={sshFor.id}
          onDone={(loaded) => {
            const { id, name } = sshFor;
            setSshFor(null);
            if (loaded) startById(id, name);
            else {
              setSummary(`"${name}" — keys not loaded; start canceled.`);
              setSummaryAttn(true);
            }
          }}
        />
      )}

      {newProject && <NewProjectModal onClose={() => setNewProject(false)} />}
      {addRepo && (
        <AddRepoModal
          onClose={() => setAddRepo(false)}
          onAdded={() => {
            setSection("repos");
            setReposReload((n) => n + 1);
          }}
        />
      )}
    </>
  );
}
