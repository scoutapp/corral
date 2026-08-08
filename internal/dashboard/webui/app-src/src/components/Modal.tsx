import type { ReactNode } from "react";

// The shared modal shell (matches index.html.tmpl's .modal-backdrop/.modal).
// Click the backdrop or the ✕ to close.
export function Modal({ title, onClose, children }: { title: string; onClose: () => void; children: ReactNode }) {
  return (
    <div className="modal-backdrop" onClick={(e) => e.target === e.currentTarget && onClose()}>
      <div className="modal" role="dialog" aria-modal="true">
        <div className="modal-head">
          <span>{title}</span>
          <button type="button" title="Close" onClick={onClose}>
            ✕
          </button>
        </div>
        <div className="modal-body">{children}</div>
      </div>
    </div>
  );
}
