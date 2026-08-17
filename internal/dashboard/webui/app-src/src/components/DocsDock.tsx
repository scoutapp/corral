import { Fragment, useEffect, useState } from "react";
import { useRouter } from "../router";
import { docsFor, type DocBlock } from "../docs/content";

// DocsDock — the app-wide "how does this page work?" pane. Mounted once at the
// App root (like ChatDock), so it persists across navigation. A slide-out drawer
// on the LEFT (mirroring the chat drawer on the right): the launcher button or
// ⌘/ opens it, Esc closes it. Content follows the current route — each page
// documents itself.

// renderInline splits a string on single-backtick `code` spans and renders those
// as <code>. No other markdown — keep it simple. **bold** is also honored since
// the copy uses it lightly for emphasis.
function renderInline(text: string): React.ReactNode {
  // First split on `code`, then within plain runs handle **bold**.
  const parts = text.split(/(`[^`]+`)/g);
  return parts.map((part, i) => {
    if (part.startsWith("`") && part.endsWith("`")) {
      return <code key={i}>{part.slice(1, -1)}</code>;
    }
    const bold = part.split(/(\*\*[^*]+\*\*)/g);
    return (
      <Fragment key={i}>
        {bold.map((b, j) =>
          b.startsWith("**") && b.endsWith("**") ? <strong key={j}>{b.slice(2, -2)}</strong> : <Fragment key={j}>{b}</Fragment>,
        )}
      </Fragment>
    );
  });
}

function Block({ b }: { b: DocBlock }) {
  if (b.h) return <h4 className="docs-h">{b.h}</h4>;
  if (b.p) return <p className="docs-p">{renderInline(b.p)}</p>;
  if (b.list)
    return (
      <ul className="docs-list">
        {b.list.map((li, i) => (
          <li key={i}>{renderInline(li)}</li>
        ))}
      </ul>
    );
  if (b.code) return <pre className="docs-code">{b.code}</pre>;
  return null;
}

export function DocsDock() {
  const { path } = useRouter();
  const [open, setOpen] = useState(false);

  // ⌘/ (or Ctrl-/) toggles; Esc closes. ⌘/ is a common "help" shortcut and
  // doesn't clash with ⌘K (the chat drawer).
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key === "/") {
        e.preventDefault();
        setOpen((o) => !o);
      } else if (e.key === "Escape" && open) {
        setOpen(false);
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [open]);

  const doc = docsFor(path);

  return (
    <>
      <button
        type="button"
        className={`docsdock-launcher${open ? " open" : ""}`}
        title="How this page works (⌘/)"
        onClick={() => setOpen((o) => !o)}
      >
        <span className="docsdock-mark">?</span>
        <span className="docsdock-launcher-label">Docs</span>
      </button>

      <aside className={`docsdock${open ? " open" : ""}`} aria-hidden={!open}>
        <header className="docsdock-head">
          <span className="docsdock-title">
            <span className="docsdock-mark">?</span> {doc.title}
          </span>
          <button type="button" className="docsdock-close" title="Close (Esc)" onClick={() => setOpen(false)}>
            ×
          </button>
        </header>
        <div className="docsdock-body">
          {doc.blocks.map((b, i) => (
            <Block key={i} b={b} />
          ))}
          <p className="docs-foot">Press <kbd>⌘/</kbd> anywhere to open these docs.</p>
        </div>
      </aside>

      {open && <div className="docsdock-scrim" onClick={() => setOpen(false)} />}
    </>
  );
}
