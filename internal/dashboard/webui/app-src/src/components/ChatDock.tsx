import { useEffect, useState } from "react";
import { ChatPanel } from "./ChatPanel";
import { GlobalConductors } from "./GlobalConductors";
import { WorkTab } from "./WorkTab";
import { useRouter, matchProject } from "../router";
import { useDragResize } from "../hooks/useDragResize";
import { usePersistentState } from "../hooks/usePersistentState";

// The dock's width is user-draggable (handle on its LEFT edge) and persisted, so
// a session with several workers/conductors isn't crammed into a narrow strip.
const DOCK_W_KEY = "corral.chatdockWidth";
const DOCK_W_DEFAULT = 440;

// ChatDock — the app-wide "Claude everywhere" pane. Mounted once at the App root
// (like the toast host), so it persists across navigation and is reachable from
// every page. A slide-out drawer on the right: ⌘K (or the launcher button) opens
// it, Esc closes it.
//
// Contents follow the page: on a PROJECT route it shows two tabs — "This project"
// (the repo-scoped /p/<id>/chat/ws) and "Global" (/chat/ws) — with the project
// chat first. Everywhere else it's just the global chat, no tabs. One surface,
// context-aware.

type Tab = "project" | "global" | "work";

// contextLabel is a short chip showing where the global chat is scoped, mirroring
// the fuller hint sent to the backend (see FirstRunChat.pageContext).
function contextLabel(path: string): string {
  let m = path.match(/^\/repos\/([^/]+)\/prs\/(\d+)/);
  if (m) return `PR #${m[2]}`;
  m = path.match(/^\/repos\/([^/]+)/);
  if (m) return `repo ${m[1]}`;
  if (path.startsWith("/logs")) return "logs";
  if (path.startsWith("/integrations")) return "integrations";
  if (path.startsWith("/automations")) return "flows";
  return "";
}

// activeTabUuid picks which host chat's conversation UUID the header shows.
function activeTabUuid(tab: string, globalUuid: string, projectUuid: string): string {
  if (tab === "project") return projectUuid;
  if (tab === "global") return globalUuid;
  return ""; // work tab (or none) — no single conversation
}

export function ChatDock() {
  const { path } = useRouter();
  const projectId = matchProject(path);

  const [open, setOpen] = useState(false);
  const [everOpened, setEverOpened] = useState(false);
  const [dockWidth, setDockWidth] = usePersistentState<number>(DOCK_W_KEY, DOCK_W_DEFAULT);
  const dockResizeRef = useDragResize({
    axis: "x",
    edge: "start", // handle on the left edge; dragging left grows the dock
    get: () => dockWidth,
    min: 340,
    max: () => Math.round(window.innerWidth * 0.9),
    onResize: setDockWidth,
  });
  // Default to the project chat when on a project (you're looking at it); the
  // global chat otherwise. Re-defaults to project whenever the project changes.
  const [tab, setTab] = useState<Tab>("project");
  const [workCount, setWorkCount] = useState(0);
  // Captured conversation UUID per host chat tab, shown in the header. These are
  // HOST conversations (this dock is the host chat); sandbox chats never render
  // a UUID. The Work tab has no single conversation, so it shows none.
  const [globalUuid, setGlobalUuid] = useState("");
  const [projectUuid, setProjectUuid] = useState("");
  useEffect(() => {
    setTab(projectId ? "project" : "global");
  }, [projectId]);

  // Poll the host-merge job count so the "Work" tab appears/updates even before
  // the dock is opened. Lightweight (a small JSON list); the WorkTab itself does
  // the richer polling + streaming once shown.
  useEffect(() => {
    let live = true;
    const poll = () => {
      fetch("/merge-jobs", { credentials: "same-origin" })
        .then((r) => (r.ok ? r.json() : { jobs: [] }))
        .then((d: { jobs?: unknown[] }) => live && setWorkCount((d.jobs || []).length))
        .catch(() => {});
    };
    poll();
    const t = setInterval(poll, 5000);
    return () => {
      live = false;
      clearInterval(t);
    };
  }, []);

  // Other surfaces (e.g. the PR "Merge with host" button) ask the dock to open
  // on the Work tab via a window event, so a launched job is immediately visible.
  useEffect(() => {
    const openWork = () => {
      setWorkCount((c) => Math.max(c, 1)); // ensure the tab renders before its poll catches up
      setTab("work");
      setOpen(true);
      setEverOpened(true);
    };
    window.addEventListener("corral:open-work", openWork);
    return () => window.removeEventListener("corral:open-work", openWork);
  }, []);

  // ⌘K / Ctrl-K toggles; Esc closes.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && (e.key === "k" || e.key === "K")) {
        e.preventDefault();
        setOpen((o) => !o);
        setEverOpened(true);
      } else if (e.key === "Escape" && open) {
        setOpen(false);
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [open]);

  function toggle() {
    setOpen((o) => !o);
    setEverOpened(true);
  }

  const showProjectTab = !!projectId;
  const showWorkTab = workCount > 0;
  // Resolve the effective tab: honor the chosen tab when it's available, else
  // fall back to global. (project only on a project route; work only when jobs
  // exist.)
  const activeTab: Tab =
    tab === "project" && showProjectTab
      ? "project"
      : tab === "work" && showWorkTab
        ? "work"
        : "global";
  const showTabs = showProjectTab || showWorkTab;
  const headerUuid = activeTabUuid(activeTab, globalUuid, projectUuid);

  return (
    <>
      <button
        type="button"
        className={`chatdock-launcher${open ? " open" : ""}`}
        title="Ask Claude (⌘K)"
        onClick={toggle}
      >
        <span className="chatdock-spark">✦</span>
        <span className="chatdock-launcher-label">Ask Claude</span>
      </button>

      <aside
        className={`chatdock${open ? " open" : ""}`}
        aria-hidden={!open}
        style={{ width: `min(${dockWidth}px, 96vw)` }}
      >
        <div className="chatdock-resize" ref={dockResizeRef} title="Drag to resize" />
        <header className="chatdock-head">
          <span className="chatdock-title">
            <span className="chatdock-spark">✦</span> Claude
            {activeTab === "global" && contextLabel(path) && (
              <span className="chatdock-ctx" title="Claude knows what page you're on">
                {contextLabel(path)}
              </span>
            )}
            {(activeTab === "global" || activeTab === "project") && headerUuid && (
              <span
                className="chatdock-uuid"
                title={`Conversation ${headerUuid} — click to copy`}
                onClick={() => navigator.clipboard?.writeText(headerUuid)}
              >
                {headerUuid.slice(0, 8)}
              </span>
            )}
          </span>
          <button type="button" className="chatdock-close" title="Close (Esc)" onClick={() => setOpen(false)}>
            ×
          </button>
        </header>

        {showTabs && (
          <div className="chatdock-tabs">
            {showProjectTab && (
              <button
                type="button"
                className={`chatdock-tab${activeTab === "project" ? " active" : ""}`}
                onClick={() => setTab("project")}
              >
                This project
              </button>
            )}
            <button
              type="button"
              className={`chatdock-tab${activeTab === "global" ? " active" : ""}`}
              onClick={() => setTab("global")}
            >
              Global
            </button>
            {showWorkTab && (
              <button
                type="button"
                className={`chatdock-tab${activeTab === "work" ? " active" : ""}`}
                onClick={() => setTab("work")}
                title="Host-merge jobs running in the background"
              >
                Work <span className="chatdock-tab-count">{workCount}</span>
              </button>
            )}
          </div>
        )}

        <div className="chatdock-body">
          {everOpened && (
            <>
              {/* Both panels stay mounted (display-toggled) so switching tabs
                  doesn't drop either conversation. The project panel only exists
                  while on a project route. */}
              {showProjectTab && (
                <div style={{ display: activeTab === "project" ? "flex" : "none", flex: 1, minHeight: 0, flexDirection: "column" }}>
                  <ChatPanel
                    wsPath={`/p/${projectId}/chat/ws?tools=Read,Grep,Glob`}
                    onConvMeta={(m) => setProjectUuid(m.convUuid)}
                  />
                </div>
              )}
              <div style={{ display: activeTab === "global" ? "flex" : "none", flex: 1, minHeight: 0, flexDirection: "column" }}>
                {/* Multiple independent global conductors, each gated by the
                    first-run capability choice before it spawns. */}
                <GlobalConductors onConvMeta={(m) => setGlobalUuid(m.convUuid)} />
              </div>
              {/* Work tab: only mounted when active, so its job viewer WS opens
                  lazily. It reports the live count back to keep the tab in sync. */}
              {activeTab === "work" && (
                <div style={{ display: "flex", flex: 1, minHeight: 0, flexDirection: "column" }}>
                  <WorkTab onCount={setWorkCount} />
                </div>
              )}
            </>
          )}
        </div>
      </aside>

      {open && <div className="chatdock-scrim" onClick={() => setOpen(false)} />}
    </>
  );
}
