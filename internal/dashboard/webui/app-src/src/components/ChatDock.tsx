import { useEffect, useState } from "react";
import { ChatPanel } from "./ChatPanel";
import { FirstRunChat } from "./FirstRunChat";
import { useRouter, matchProject } from "../router";

// ChatDock — the app-wide "Claude everywhere" pane. Mounted once at the App root
// (like the toast host), so it persists across navigation and is reachable from
// every page. A slide-out drawer on the right: ⌘K (or the launcher button) opens
// it, Esc closes it.
//
// Contents follow the page: on a PROJECT route it shows two tabs — "This project"
// (the repo-scoped /p/<id>/chat/ws) and "Global" (/chat/ws) — with the project
// chat first. Everywhere else it's just the global chat, no tabs. One surface,
// context-aware.

type Tab = "project" | "global";

export function ChatDock() {
  const { path } = useRouter();
  const projectId = matchProject(path);

  const [open, setOpen] = useState(false);
  const [everOpened, setEverOpened] = useState(false);
  // Default to the project chat when on a project (you're looking at it); the
  // global chat otherwise. Re-defaults to project whenever the project changes.
  const [tab, setTab] = useState<Tab>("project");
  useEffect(() => {
    setTab(projectId ? "project" : "global");
  }, [projectId]);

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
  const activeTab: Tab = showProjectTab ? tab : "global";

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

      <aside className={`chatdock${open ? " open" : ""}`} aria-hidden={!open}>
        <header className="chatdock-head">
          <span className="chatdock-title">
            <span className="chatdock-spark">✦</span> Claude
          </span>
          <button type="button" className="chatdock-close" title="Close (Esc)" onClick={() => setOpen(false)}>
            ×
          </button>
        </header>

        {showProjectTab && (
          <div className="chatdock-tabs">
            <button
              type="button"
              className={`chatdock-tab${activeTab === "project" ? " active" : ""}`}
              onClick={() => setTab("project")}
            >
              This project
            </button>
            <button
              type="button"
              className={`chatdock-tab${activeTab === "global" ? " active" : ""}`}
              onClick={() => setTab("global")}
            >
              Global
            </button>
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
                  <ChatPanel wsPath={`/p/${projectId}/chat/ws?tools=Read,Grep,Glob`} />
                </div>
              )}
              <div style={{ display: activeTab === "global" ? "flex" : "none", flex: 1, minHeight: 0, flexDirection: "column" }}>
                {/* The global chat's capability is a first-run choice; gate the
                    panel behind it so we prompt before spawning the assistant. */}
                <FirstRunChat />
              </div>
            </>
          )}
        </div>
      </aside>

      {open && <div className="chatdock-scrim" onClick={() => setOpen(false)} />}
    </>
  );
}
