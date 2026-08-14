import { useEffect, useRef, useState } from "react";
import { Link } from "../router";
import type { useDnd } from "../hooks/useDnd";

// DndControl is the overview header widget: a 🌙 (quiet) / 🔔 (notifying) button
// that opens a small menu to temporarily allow notifications (snooze) for a
// preset period, or end an active snooze. Full config lives in Global settings.

type Dnd = ReturnType<typeof useDnd>;

export function DndControl({ dnd }: { dnd: Dnd }) {
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement | null>(null);

  // Close on outside click.
  useEffect(() => {
    if (!open) return;
    const onDoc = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) setOpen(false);
    };
    document.addEventListener("mousedown", onDoc);
    return () => document.removeEventListener("mousedown", onDoc);
  }, [open]);

  const label = dnd.quiet ? "🌙 quiet" : dnd.snoozeActive ? "🔔 allowed" : "🔔 alerts";
  const title = dnd.quiet
    ? "Do Not Disturb is active (quiet hours). Click to allow notifications."
    : dnd.snoozeActive
      ? "Notifications temporarily allowed. Click to manage."
      : "Notifications on. Click to manage Do Not Disturb.";

  const allow = (fn: () => void) => {
    fn();
    setOpen(false);
  };

  return (
    <div className="dnd-control" ref={ref}>
      <button
        type="button"
        className={`btn dnd-btn${dnd.quiet ? " quiet" : ""}`}
        title={title}
        aria-pressed={dnd.quiet}
        onClick={() => setOpen((o) => !o)}
      >
        {label} ▾
      </button>

      {open && (
        <div className="dnd-menu" role="menu">
          {dnd.snoozeActive ? (
            <>
              <div className="dnd-menu-head">Notifications allowed</div>
              <button type="button" className="dnd-menu-item" onClick={() => allow(dnd.clearSnooze)}>
                End now (back to Do Not Disturb)
              </button>
            </>
          ) : (
            <>
              <div className="dnd-menu-head">Allow notifications</div>
              <button type="button" className="dnd-menu-item" onClick={() => allow(() => dnd.snoozeFor(15))}>
                for 15 minutes
              </button>
              <button type="button" className="dnd-menu-item" onClick={() => allow(() => dnd.snoozeFor(60))}>
                for 1 hour
              </button>
              <button type="button" className="dnd-menu-item" onClick={() => allow(dnd.snoozeUntilOff)}>
                until I turn it off
              </button>
            </>
          )}
          <div className="dnd-menu-sep" />
          <Link to="/global" className="dnd-menu-item dnd-menu-link" onClick={() => setOpen(false)}>
            Manage in Global settings →
          </Link>
        </div>
      )}
    </div>
  );
}
