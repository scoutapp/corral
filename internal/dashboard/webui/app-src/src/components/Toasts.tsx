import { createContext, useCallback, useContext, useEffect, useRef, useState, type ReactNode } from "react";
import { Link } from "../router";

// Cross-project toasts: when a project OTHER than the one you're viewing goes
// working -> waiting (it just finished and needs you), pop a dismissible toast
// linking to it. Ctrl-. clears all toasts. Ported from toasts.js; the working
// ->waiting detection is driven by whatever polls /status and calls notify().

interface ToastItem {
  key: number;
  id: string;
  name: string;
}

interface ToastCtx {
  // notify raises a "waiting on you" toast for a project (unless suppressed).
  notify: (id: string, name: string) => void;
  clearAll: () => void;
}

const Ctx = createContext<ToastCtx>({ notify: () => {}, clearAll: () => {} });

export function useToasts() {
  return useContext(Ctx);
}

export function ToastProvider({ children }: { children: ReactNode }) {
  const [items, setItems] = useState<ToastItem[]>([]);
  const nextKey = useRef(1);

  const remove = useCallback((key: number) => {
    setItems((xs) => xs.filter((t) => t.key !== key));
  }, []);

  const notify = useCallback(
    (id: string, name: string) => {
      const key = nextKey.current++;
      setItems((xs) => [...xs, { key, id, name }]);
      // Auto-dismiss after a while (long enough to notice/click).
      window.setTimeout(() => remove(key), 13000);
    },
    [remove],
  );

  const clearAll = useCallback(() => setItems([]), []);

  // Ctrl-. clears all toasts (matches the old sandclaude:clear-notifications).
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "." && (e.ctrlKey || e.metaKey)) clearAll();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [clearAll]);

  return (
    <Ctx.Provider value={{ notify, clearAll }}>
      {children}
      <div className="toast-host">
        {items.map((t) => (
          <Link key={t.key} className="toast" to={`/p/${t.id}/`}>
            <span className="toast-dot" />
            <span className="toast-body">
              <strong>{t.name}</strong>
              <span className="toast-sub">is waiting on you</span>
            </span>
            <button
              className="toast-x"
              title="Dismiss"
              aria-label="Dismiss"
              onClick={(e) => {
                e.preventDefault();
                e.stopPropagation();
                remove(t.key);
              }}
            >
              ✕
            </button>
          </Link>
        ))}
      </div>
    </Ctx.Provider>
  );
}
