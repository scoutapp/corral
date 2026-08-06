import { useEffect } from "react";

// Toggle a class on <body> for the lifetime of a page. The landing/global pages
// render under `body.console`, which is where the console palette CSS variables
// (--con-panel, --con-line, --con-off, …) and the page background/font live —
// see styles/dashboard.css. The per-project page deliberately does NOT use the
// console class (it themes off :root instead), so each page opts in explicitly.
export function useBodyClass(className: string) {
  useEffect(() => {
    document.body.classList.add(className);
    return () => document.body.classList.remove(className);
  }, [className]);
}
