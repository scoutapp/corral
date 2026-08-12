import { useCallback, useEffect, useMemo, useState } from "react";
import { Link } from "../router";
import { useStatus } from "../hooks/useStatus";
import { usePersistentState } from "../hooks/usePersistentState";
import { useDragResize } from "../hooks/useDragResize";
import { postRaw } from "../api/client";
import { SSHLoadModal } from "../components/SSHLoadModal";
import { XtermPane } from "../components/XtermPane";
import { ChatPanel } from "../components/ChatPanel";
import { FilesTab } from "../tabs/FilesTab";
import { DiffTab } from "../tabs/DiffTab";
import { ConfigTab } from "../tabs/ConfigTab";
import { MitmTab } from "../tabs/MitmTab";
import { FirewallTab } from "../tabs/FirewallTab";

type Tab = "files" | "diff" | "container" | "mitm" | "firewall" | "config";

const TABS: { key: Tab; label: string }[] = [
  { key: "files", label: "Files" },
  { key: "diff", label: "Diff" },
  { key: "container", label: "Container" },
  { key: "mitm", label: "Mitm Proxy" },
  { key: "firewall", label: "Firewall Log" },
  { key: "config", label: "Config" },
];

const DOCK_KEY = "corral.dockCollapsed";
const CHAT_DOCK_KEY = "corral.chatDock";
// Panel sizes are shared across projects (a global preference); open-state is
// per-project (keyed by id) so each project remembers whether you had its host
// shell / chat open. Defaults roughly match the CSS starting sizes.
const DOCK_W_KEY = "corral.dockWidth";
const HOST_H_KEY = "corral.hostHeight";
const CHAT_W_KEY = "corral.chatWidth";
const DOCK_W_DEFAULT = 480;
const HOST_H_DEFAULT = 320;
const CHAT_W_DEFAULT = 420;

export function ProjectPage({ id }: { id: string }) {
  const { projects } = useStatus(4000);
  const me = useMemo(() => projects.find((p) => p.id === id), [projects, id]);
  const name = me?.name || id;

  const [tab, setTab] = useState<Tab>("files");
  // Lazily mount a tab's content only after first activation (mirrors the old
  // lazy-init so no PTY/stream starts until the tab is opened), then keep it
  // mounted but hidden so its state (editor, scroll) survives tab switches.
  const [seen, setSeen] = useState<Record<Tab, boolean>>({
    files: true,
    diff: false,
    container: false,
    mitm: false,
    firewall: false,
    config: false,
  });
  // Bumped each time the Config tab is (re)activated, so ConfigTab re-fetches
  // fresh /config on open — picking up e.g. a host monitored from the Mitm tab.
  const [configRefresh, setConfigRefresh] = useState(0);
  const activate = (t: Tab) => {
    setTab(t);
    setSeen((s) => (s[t] ? s : { ...s, [t]: true }));
    if (t === "config") setConfigRefresh((n) => n + 1);
  };

  // Docks. Collapsed/side are global prefs; open-state is per-project so each
  // project remembers whether you left its host shell / chat open (survives
  // navigation + reload). Sizes are global prefs.
  const [dockCollapsed, setDockCollapsed] = usePersistentState<boolean>(DOCK_KEY, false);
  const [dockSeen, setDockSeen] = useState(!dockCollapsed);
  const [hostOpen, setHostOpen] = usePersistentState<boolean>(`corral.hostOpen.${id}`, false);
  const [chatOpen, setChatOpen] = usePersistentState<boolean>(`corral.chatOpen.${id}`, false);
  const [chatSide, setChatSide] = usePersistentState<"left" | "right">(CHAT_DOCK_KEY, "right");

  // Persistent, drag-resizable panel sizes.
  const [dockWidth, setDockWidth] = usePersistentState<number>(DOCK_W_KEY, DOCK_W_DEFAULT);
  const [hostHeight, setHostHeight] = usePersistentState<number>(HOST_H_KEY, HOST_H_DEFAULT);
  const [chatWidth, setChatWidth] = usePersistentState<number>(CHAT_W_KEY, CHAT_W_DEFAULT);

  const dockResizeRef = useDragResize({
    axis: "x", edge: "start", get: () => dockWidth, min: 240,
    max: () => Math.round(window.innerWidth * 0.8), onResize: setDockWidth,
  });
  const hostResizeRef = useDragResize({
    axis: "y", edge: "start", get: () => hostHeight, min: 120,
    max: () => Math.round(window.innerHeight * 0.85), onResize: setHostHeight,
  });
  const chatResizeRef = useDragResize({
    axis: "x", edge: chatSide === "right" ? "start" : "end", get: () => chatWidth, min: 300,
    max: () => Math.round(window.innerWidth * 0.8), onResize: setChatWidth,
  });

  const toggleDock = useCallback(() => {
    setDockCollapsed((c) => {
      const next = !c;
      if (!next) setDockSeen(true);
      return next;
    });
  }, [setDockCollapsed]);
  const toggleHost = useCallback(() => setHostOpen((v) => !v), [setHostOpen]);
  const toggleChat = useCallback(() => setChatOpen((v) => !v), [setChatOpen]);

  // Power toggle: mirrors dashboard.js — a pending spinner that clears when the
  // /status poll confirms the container reached the target state.
  const [pending, setPending] = useState<"" | "starting" | "stopping">("");
  const [sshOpen, setSshOpen] = useState(false);
  const up = !!me?.container_up;

  useEffect(() => {
    if (!pending) return;
    const want = pending === "starting";
    if (up === want) setPending("");
  }, [up, pending]);

  const doPower = useCallback(
    async (forceStart = false) => {
      const stopping = forceStart ? false : up;
      setPending(stopping ? "stopping" : "starting");
      try {
        const res = await postRaw(`/p/${id}/${stopping ? "stop" : "start"}`);
        const body = await res.json().catch(() => ({}));
        if (!stopping && res.status === 409 && body?.ssh_keys_pending) {
          setPending("");
          setSshOpen(true);
          return;
        }
        if (res.status >= 400) throw new Error(body?.message || `HTTP ${res.status}`);
        // Stay pending; the status poll clears it on target state.
      } catch (e) {
        setPending("");
        alert(`${stopping ? "stop" : "start"} failed: ${(e as Error).message}`);
      }
    },
    [id, up],
  );

  // Hotkeys (Cmd/Ctrl-J host, -K chat, -; dock).
  useEffect(() => {
    const typing = () => {
      const el = document.activeElement as HTMLElement | null;
      if (!el) return false;
      const tag = (el.tagName || "").toLowerCase();
      return tag === "input" || tag === "textarea" || el.isContentEditable || el.classList.contains("xterm-helper-textarea");
    };
    const onKey = (e: KeyboardEvent) => {
      if (!(e.metaKey || e.ctrlKey) || e.altKey) return;
      if (typing()) return;
      const k = (e.key || "").toLowerCase();
      if (k === "j") {
        e.preventDefault();
        toggleHost();
      } else if (k === "k") {
        e.preventDefault();
        toggleChat();
      } else if (k === ";") {
        e.preventDefault();
        toggleDock();
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [toggleHost, toggleChat, toggleDock]);

  const flipChatSide = () => setChatSide((s) => (s === "left" ? "right" : "left"));

  return (
    <div className="project-page">
      <header>
          <Link to="/" className="back">
            ← All projects
          </Link>
          <h1>{name}</h1>
          <button
            className={`dock-toggle power-toggle${up ? " is-up" : ""}${pending ? " busy" : ""}`}
            type="button"
            disabled={!!pending}
            title="Start or stop this project's container"
            onClick={() => doPower(false)}
          >
            {pending ? (
              <>
                <span className="power-spin">↻</span> {pending === "starting" ? "starting…" : "stopping…"}
              </>
            ) : up ? (
              "■ Stop"
            ) : (
              "▶ Start"
            )}
          </button>
          <button className={`dock-toggle${!dockCollapsed ? " on" : ""}`} type="button" title="Show/hide the Claude terminal" onClick={toggleDock}>
            ⌨ Terminal
          </button>
          <button className={`dock-toggle${hostOpen ? " on" : ""}`} type="button" title="Show/hide a host shell (⌘J)" onClick={toggleHost}>
            ▂ Host shell
          </button>
          <button className={`dock-toggle${chatOpen ? " on" : ""}`} type="button" title="Ask Claude — a host claude chat (⌘K)" onClick={toggleChat}>
            💬 Ask Claude
          </button>
      </header>

      <div className={`project-layout${dockCollapsed ? " dock-collapsed" : ""}`} id="project-layout">
        <div className="tab-area">
          <div className="tabs">
            {TABS.map((t) => (
              <button key={t.key} className={`tab-btn${tab === t.key ? " active" : ""}`} onClick={() => activate(t.key)}>
                {t.label}
              </button>
            ))}
          </div>

          <div id="tab-files" className="tab-panel" style={{ display: tab === "files" ? "flex" : "none", flex: "1 1 auto", minHeight: 0 }}>
            {seen.files && <FilesTab projectId={id} />}
          </div>
          <div id="tab-diff" className="tab-panel" style={{ display: tab === "diff" ? "flex" : "none", flex: "1 1 auto", minHeight: 0 }}>
            {seen.diff && <DiffTab projectId={id} />}
          </div>
          <div id="tab-container" className="tab-panel" style={{ display: tab === "container" ? "flex" : "none", flex: "1 1 auto", minHeight: 0 }}>
            <div className="screen-frame screen-frame-fill">
              <div className="screen-bar">
                <i className="screen-dot" />
                container shell · {name}
              </div>
              {seen.container && up ? (
                <XtermPane projectId={id} wsPath="/container/ws" kind="container" />
              ) : (
                <p className="empty">{up ? "open to connect" : "container not running"}</p>
              )}
            </div>
          </div>
          <div id="tab-mitm" className="tab-panel" style={{ display: tab === "mitm" ? "block" : "none" }}>
            {seen.mitm && <MitmTab projectId={id} mitmUp={!!me?.mitm_up} />}
          </div>
          <div id="tab-firewall" className="tab-panel" style={{ display: tab === "firewall" ? "block" : "none" }}>
            {seen.firewall && <FirewallTab projectId={id} />}
          </div>
          <div id="tab-config" className="tab-panel" style={{ display: tab === "config" ? "block" : "none", flex: "1 1 auto", minHeight: 0 }}>
            {seen.config && <ConfigTab projectId={id} refreshKey={configRefresh} />}
          </div>
        </div>

        <aside className="term-dock" id="term-dock" style={{ flex: `0 0 ${dockWidth}px` }}>
          <div className="term-dock-handle" ref={dockResizeRef} title="Drag to resize" />
          <div className="screen-frame">
            <div className="screen-bar">
              <i className="screen-dot" />
              {name} · Claude
            </div>
            {dockSeen && me?.tmux_up ? (
              <XtermPane projectId={id} wsPath="/terminal/ws" kind="claude" />
            ) : (
              <p className="empty">
                {me?.tmux_up ? "open to connect" : "this project isn't running — press ▶ Start above"}
              </p>
            )}
          </div>
        </aside>
      </div>

      {hostOpen && (
        <div className="host-overlay" id="host-overlay" style={{ height: `${hostHeight}px` }}>
          <div className="host-overlay-handle" ref={hostResizeRef} title="Drag to resize" />
          <div className="host-overlay-bar">
            <span>
              <i className="screen-dot" />
              host shell · {name}
            </span>
            <button type="button" title="Close (⌘J)" onClick={() => setHostOpen(false)}>
              ✕
            </button>
          </div>
          <div className="host-overlay-iframe">
            <XtermPane projectId={id} wsPath="/host/ws" kind="host" />
          </div>
        </div>
      )}

      {chatOpen && (
        <div className={`chat-panel${chatSide === "left" ? " dock-left" : ""}`} id="chat-panel" style={{ width: `${chatWidth}px` }}>
          <div className="chat-panel-handle" ref={chatResizeRef} />
          <div className="chat-panel-bar">
            <span>
              <i className="screen-dot" />
              ask claude · host
            </span>
            <span className="chat-panel-actions">
              <button type="button" title="Dock left/right" onClick={flipChatSide}>
                ⇄
              </button>
              <button type="button" title="Close (⌘K)" onClick={() => setChatOpen(false)}>
                ✕
              </button>
            </span>
          </div>
          <div className="chat-panel-iframe">
            <ChatPanel projectId={id} />
          </div>
        </div>
      )}

      {sshOpen && (
        <SSHLoadModal
          projectId={id}
          onDone={(loaded) => {
            setSshOpen(false);
            if (loaded) doPower(true);
          }}
        />
      )}
    </div>
  );
}
