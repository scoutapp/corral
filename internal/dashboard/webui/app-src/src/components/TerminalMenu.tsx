import { useEffect, useRef } from "react";

export type TermAction = "split-h" | "split-v" | "kill-pane" | "copy" | "paste" | "clear";

// Right-click context menu for a terminal pane. Split/close are only shown for
// tmux-backed terminals (canSplit); copy/paste/clear are always available. The
// menu closes on outside click, Escape, or after an action.
export function TerminalMenu({
  x,
  y,
  canSplit,
  onAction,
  onClose,
}: {
  x: number;
  y: number;
  canSplit: boolean;
  onAction: (a: TermAction) => void;
  onClose: () => void;
}) {
  const ref = useRef<HTMLDivElement | null>(null);
  useEffect(() => {
    const onDown = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) onClose();
    };
    const onKey = (e: KeyboardEvent) => e.key === "Escape" && onClose();
    document.addEventListener("mousedown", onDown);
    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("mousedown", onDown);
      document.removeEventListener("keydown", onKey);
    };
  }, [onClose]);

  // Keep the menu on-screen if opened near an edge.
  const style: React.CSSProperties = {
    left: Math.min(x, window.innerWidth - 190),
    top: Math.min(y, window.innerHeight - 220),
  };

  return (
    <div className="term-menu" ref={ref} style={style} onContextMenu={(e) => e.preventDefault()}>
      {canSplit && (
        <>
          <button className="term-menu-item" onClick={() => onAction("split-h")}>
            Split right
          </button>
          <button className="term-menu-item" onClick={() => onAction("split-v")}>
            Split down
          </button>
          <div className="term-menu-sep" />
        </>
      )}
      <button className="term-menu-item" onClick={() => onAction("copy")}>
        Copy <span className="term-menu-key">⌘C</span>
      </button>
      <button className="term-menu-item" onClick={() => onAction("paste")}>
        Paste <span className="term-menu-key">⌘V</span>
      </button>
      <button className="term-menu-item" onClick={() => onAction("clear")}>
        Clear
      </button>
      {canSplit && (
        <>
          <div className="term-menu-sep" />
          <button className="term-menu-item term-menu-danger" onClick={() => onAction("kill-pane")}>
            Close pane
          </button>
        </>
      )}
    </div>
  );
}
