import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Link } from "../router";
import { useStatus } from "../hooks/useStatus";
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

const DOCK_KEY = "sandclaude.dockCollapsed";
const CHAT_DOCK_KEY = "sandclaude.chatDock";

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

  // Docks
  const [dockCollapsed, setDockCollapsed] = useState<boolean>(() => {
    try {
      return localStorage.getItem(DOCK_KEY) === "1";
    } catch {
      return false;
    }
  });
  const [dockSeen, setDockSeen] = useState(!dockCollapsed);
  const [hostOpen, setHostOpen] = useState(false);
  const [chatOpen, setChatOpen] = useState(false);
  const [chatSide, setChatSide] = useState<"left" | "right">(() => {
    try {
      return (localStorage.getItem(CHAT_DOCK_KEY) as "left" | "right") || "right";
    } catch {
      return "right";
    }
  });

  const toggleDock = useCallback(() => {
    setDockCollapsed((c) => {
      const next = !c;
      try {
        localStorage.setItem(DOCK_KEY, next ? "1" : "0");
      } catch {
        /* ignore */
      }
      if (!next) setDockSeen(true);
      return next;
    });
  }, []);
  const toggleHost = useCallback(() => setHostOpen((v) => !v), []);
  const toggleChat = useCallback(() => setChatOpen((v) => !v), []);

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

  const flipChatSide = () => {
    setChatSide((s) => {
      const next = s === "left" ? "right" : "left";
      try {
        localStorage.setItem(CHAT_DOCK_KEY, next);
      } catch {
        /* ignore */
      }
      return next;
    });
  };

  const hostHandleRef = useRef<HTMLDivElement | null>(null);

  return (
    <>
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
          <div id="tab-container" className="tab-panel" style={{ display: tab === "container" ? "block" : "none" }}>
            <div className="screen-frame">
              <div className="screen-bar">
                <i className="screen-dot" />
                container shell · {name}
              </div>
              {seen.container && up ? (
                <XtermPane projectId={id} wsPath="/container/ws" />
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

        <aside className="term-dock" id="term-dock">
          <div className="screen-frame">
            <div className="screen-bar">
              <i className="screen-dot" />
              {name} · Claude
            </div>
            {dockSeen && me?.tmux_up ? (
              <XtermPane projectId={id} wsPath="/terminal/ws" />
            ) : (
              <p className="empty">
                {me?.tmux_up ? "open to connect" : "this project isn't running — press ▶ Start above"}
              </p>
            )}
          </div>
        </aside>
      </div>

      {hostOpen && (
        <div className="host-overlay" id="host-overlay">
          <div className="host-overlay-handle" ref={hostHandleRef} />
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
            <XtermPane projectId={id} wsPath="/host/ws" />
          </div>
        </div>
      )}

      {chatOpen && (
        <div className={`chat-panel${chatSide === "left" ? " dock-left" : ""}`} id="chat-panel">
          <div className="chat-panel-handle" />
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
    </>
  );
}
