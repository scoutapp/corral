import { useEffect, useState } from "react";
import { ChatPanel } from "./ChatPanel";

// ChatDock — the app-wide "Claude everywhere" pane. Mounted once at the App root
// (like the toast host), so it persists across navigation and is reachable from
// every page. A slide-out drawer on the right: ⌘K (or the launcher button) opens
// it, Esc closes it.
//
// This PR (shell) hosts the GLOBAL chat (/chat/ws). The next PR gives it a tab
// bar on project pages — "This project" (the repo-scoped chat) alongside "Global"
// — absorbing today's per-project ChatPanel into this one surface.

export function ChatDock() {
  const [open, setOpen] = useState(false);
  // Mount the WebSocket-backed panel only once opened, and keep it mounted after
  // (so the conversation survives close/reopen) — but not before first open, to
  // avoid a chat process for users who never touch it.
  const [everOpened, setEverOpened] = useState(false);

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

  return (
    <>
      {/* Corner launcher — present on every page. */}
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
        <div className="chatdock-body">
          {everOpened && <ChatPanel wsPath="/chat/ws" />}
        </div>
      </aside>

      {/* Click-off scrim (doesn't dim the page much; just captures the click). */}
      {open && <div className="chatdock-scrim" onClick={() => setOpen(false)} />}
    </>
  );
}
