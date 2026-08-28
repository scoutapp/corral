import { Fragment, useEffect, useState } from "react";
import { useRouter } from "../router";
import { DOC_PAGES, docEntryFor, type DocBlock } from "../docs/content";

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

function Block({ b, onZoom }: { b: DocBlock; onZoom?: (img: { src: string; alt: string }) => void }) {
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
  if (b.img) {
    const img = b.img;
    return (
      <button type="button" className="docs-shot-wrap" onClick={() => onZoom?.(img)} title="Click to enlarge">
        <img className="docs-shot" src={img.src} alt={img.alt} loading="lazy" />
        <span className="docs-shot-zoom" aria-hidden="true">⤢ Zoom</span>
      </button>
    );
  }
  return null;
}

// Lightbox — a full-screen overlay showing one screenshot at full size. Click the
// backdrop / ✕ or press Esc to close.
function Lightbox({ img, onClose }: { img: { src: string; alt: string }; onClose: () => void }) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        e.stopPropagation();
        onClose();
      }
    };
    // Capture phase so Esc closes the lightbox before the drawer sees it.
    window.addEventListener("keydown", onKey, true);
    return () => window.removeEventListener("keydown", onKey, true);
  }, [onClose]);

  return (
    <div className="docs-lightbox" onClick={onClose} role="dialog" aria-modal="true" aria-label={img.alt}>
      <button type="button" className="docs-lightbox-close" title="Close (Esc)" onClick={onClose}>
        ×
      </button>
      <img className="docs-lightbox-img" src={img.src} alt={img.alt} onClick={(e) => e.stopPropagation()} />
      <p className="docs-lightbox-cap">{img.alt}</p>
    </div>
  );
}

export function DocsDock() {
  const { path } = useRouter();
  const [open, setOpen] = useState(false);
  // The page whose docs are shown. null = follow the current route (the default);
  // clicking a sidebar item pins an explicit key that overrides the route until
  // the drawer is reopened (⌘/ / launcher reset it back to the route default).
  const [selectedKey, setSelectedKey] = useState<string | null>(null);
  // The screenshot currently enlarged in the lightbox (null = none).
  const [zoomed, setZoomed] = useState<{ src: string; alt: string } | null>(null);

  // The page the current ROUTE documents (for the default + highlighting).
  const routeKey = docEntryFor(path).key;

  const openDrawer = (o: boolean | ((o: boolean) => boolean)) => {
    const next = typeof o === "function" ? o(open) : o;
    if (next) setSelectedKey(null); // opening → start on the current page's docs
    setOpen(next);
  };

  // ⌘/ (or Ctrl-/) toggles; Esc closes. ⌘/ is a common "help" shortcut and
  // doesn't clash with ⌘K (the chat drawer).
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key === "/") {
        e.preventDefault();
        openDrawer((o) => !o);
      } else if (e.key === "Escape" && open) {
        setOpen(false);
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open]);

  const activeKey = selectedKey ?? routeKey;
  const active = DOC_PAGES.find((e) => e.key === activeKey) ?? DOC_PAGES[0];
  const doc = active.page;

  return (
    <>
      <button
        type="button"
        className={`docsdock-launcher${open ? " open" : ""}`}
        title="Docs (⌘/)"
        onClick={() => openDrawer((o) => !o)}
      >
        <span className="docsdock-mark">?</span>
        <span className="docsdock-launcher-label">Docs</span>
      </button>

      <aside className={`docsdock${open ? " open" : ""}`} aria-hidden={!open}>
        <header className="docsdock-head">
          <span className="docsdock-title">
            <span className="docsdock-mark">?</span> Docs
          </span>
          <button type="button" className="docsdock-close" title="Close (Esc)" onClick={() => setOpen(false)}>
            ×
          </button>
        </header>
        <div className="docsdock-cols">
          <nav className="docsdock-nav" aria-label="All docs">
            {DOC_PAGES.map((e) => (
              <button
                key={e.key}
                type="button"
                className={`docsdock-navitem${e.key === activeKey ? " active" : ""}${e.key === routeKey ? " current" : ""}`}
                onClick={() => setSelectedKey(e.key)}
                title={e.key === routeKey ? "The page you're on" : undefined}
              >
                {e.page.title}
                {e.key === routeKey && <span className="docsdock-here">•</span>}
              </button>
            ))}
          </nav>
          <div className="docsdock-body">
            <h3 className="docsdock-pagetitle">{doc.title}</h3>
            {doc.blocks.map((b, i) => (
              <Block key={i} b={b} onZoom={setZoomed} />
            ))}
            {doc.more && (
              <p className="docs-more">
                Full guide: <code>{doc.more}</code> in the repo.
              </p>
            )}
            <p className="docs-foot">
              Press <kbd>⌘/</kbd> anywhere to open docs. Pick any page on the left.
            </p>
          </div>
        </div>
      </aside>

      {open && <div className="docsdock-scrim" onClick={() => setOpen(false)} />}
      {zoomed && <Lightbox img={zoomed} onClose={() => setZoomed(null)} />}
    </>
  );
}
