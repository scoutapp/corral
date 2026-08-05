import { createContext, useCallback, useContext, useEffect, useState, type ReactNode } from "react";

// Minimal History-API router. The app has three real routes — "/", "/global",
// and "/p/:id" — mirroring the server paths so a hard reload on any of them
// still lands on the SPA shell (the Go server serves index.html for all three).
// A dependency-free router keeps the committed bundle small and reviewable.

interface RouterCtx {
  path: string;
  navigate: (to: string) => void;
}

const Ctx = createContext<RouterCtx>({ path: "/", navigate: () => {} });

export function RouterProvider({ children }: { children: ReactNode }) {
  const [path, setPath] = useState(() => window.location.pathname);

  useEffect(() => {
    const onPop = () => setPath(window.location.pathname);
    window.addEventListener("popstate", onPop);
    return () => window.removeEventListener("popstate", onPop);
  }, []);

  const navigate = useCallback((to: string) => {
    if (to === window.location.pathname) return;
    window.history.pushState({}, "", to);
    setPath(to);
  }, []);

  return <Ctx.Provider value={{ path, navigate }}>{children}</Ctx.Provider>;
}

export function useRouter() {
  return useContext(Ctx);
}

// Link renders an <a> that intercepts clicks for client-side navigation while
// preserving normal browser behavior for modified clicks (new tab, etc.).
export function Link({ to, children, ...rest }: { to: string; children: ReactNode } & React.AnchorHTMLAttributes<HTMLAnchorElement>) {
  const { navigate } = useRouter();
  return (
    <a
      href={to}
      onClick={(e) => {
        if (e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
        e.preventDefault();
        navigate(to);
      }}
      {...rest}
    >
      {children}
    </a>
  );
}

// matchProject returns the project id when path is /p/<id>[/...], else null.
export function matchProject(path: string): string | null {
  const m = path.match(/^\/p\/([^/]+)/);
  return m ? m[1] : null;
}
