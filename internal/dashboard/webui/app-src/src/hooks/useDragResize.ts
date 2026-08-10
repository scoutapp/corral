import { useCallback, useRef } from "react";

// useDragResize wires a drag handle so pointer-dragging it resizes a panel along
// one axis, reporting the new size (clamped) to `onResize`. It's dimension-only:
// the caller owns where the size is stored (usually persistent state) and applies
// it as a style. `axis` picks the pointer delta; `edge` picks the sign so the
// panel grows the intuitive way regardless of which side the handle sits on:
//   - Claude dock: handle on the LEFT edge, dragging left grows it  -> edge "start", axis "x"
//   - host overlay: handle on TOP, dragging up grows it             -> edge "start", axis "y"
//   - chat panel: handle on the inner edge                          -> edge depends on side
export interface DragResizeOpts {
  axis: "x" | "y";
  edge: "start" | "end"; // "start": size increases as pointer moves toward smaller coord
  get: () => number; // current size at drag start
  min: number;
  max: () => number; // usually a fraction of window width/height
  onResize: (size: number) => void;
}

// Returns a ref callback for the handle element. Latest opts are read through a
// ref so the handler always sees current get/onResize without re-attaching.
export function useDragResize(opts: DragResizeOpts) {
  const optsRef = useRef(opts);
  optsRef.current = opts;

  return useCallback((el: HTMLDivElement | null) => {
    if (!el) return; // unmount: listeners were bound to the old (now detached) node
    const onPointerDown = (e: PointerEvent) => {
      e.preventDefault();
      const o = optsRef.current;
      const startPos = o.axis === "x" ? e.clientX : e.clientY;
      const startSize = o.get();
      el.setPointerCapture(e.pointerId);
      document.body.style.userSelect = "none";
      document.body.style.cursor = o.axis === "x" ? "ew-resize" : "ns-resize";

      const onMove = (ev: PointerEvent) => {
        const cur = optsRef.current;
        const pos = cur.axis === "x" ? ev.clientX : ev.clientY;
        let delta = pos - startPos;
        // "start" edge (handle on the smaller-coord side): moving toward smaller
        // coordinates (negative delta) GROWS the panel.
        if (cur.edge === "start") delta = -delta;
        cur.onResize(Math.max(cur.min, Math.min(cur.max(), startSize + delta)));
      };
      const onUp = (ev: PointerEvent) => {
        try {
          el.releasePointerCapture(ev.pointerId);
        } catch {
          /* ignore */
        }
        document.body.style.userSelect = "";
        document.body.style.cursor = "";
        el.removeEventListener("pointermove", onMove);
        el.removeEventListener("pointerup", onUp);
      };
      el.addEventListener("pointermove", onMove);
      el.addEventListener("pointerup", onUp);
    };
    el.addEventListener("pointerdown", onPointerDown);
  }, []);
}
