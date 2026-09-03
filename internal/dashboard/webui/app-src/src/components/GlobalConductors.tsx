import { useState } from "react";
import { FirstRunChat } from "./FirstRunChat";
import { useDragResize } from "../hooks/useDragResize";
import { usePersistentState } from "../hooks/usePersistentState";

// GlobalConductors is the ChatDock's "Global" surface: like the Work tab, but for
// the app-wide Claude. You can run SEVERAL independent global conductors at once —
// each is its own host-Claude session (own transcript + resume), listed in a left
// rail with a "+ New". The backend already supports this: every /chat/ws
// connection is an independent conversation, so multiple panels run concurrently.
//
// Each conductor's transcript + Claude session id persist under a per-conductor
// localStorage key, so a reload restores every conductor and continues its
// session. The list of conductor ids is itself persisted.

type Conductor = { id: string; label: string };

const LIST_KEY = "corral.conductors";
const RAIL_W_KEY = "corral.conductorRailWidth";
const RAIL_W_DEFAULT = 150;

// A stable-ish id without Date.now()/Math.random() churn concerns for keys: a
// counter folded into the persisted list. New ids are max(existing)+1.
function nextId(list: Conductor[]): string {
  let max = 0;
  for (const c of list) {
    const n = parseInt(c.id.replace(/\D/g, ""), 10);
    if (!Number.isNaN(n) && n > max) max = n;
  }
  return `c${max + 1}`;
}

export function GlobalConductors({ onConvMeta }: { onConvMeta?: (meta: { convId: number; convUuid: string }) => void }) {
  // Seed with one conductor. Its persistKey is "global" so it inherits any
  // existing single-conductor transcript from before this multi-conductor UI.
  const [list, setList] = usePersistentState<Conductor[]>(LIST_KEY, [{ id: "c1", label: "Conductor 1" }]);
  const [active, setActive] = useState<string>(() => list[0]?.id || "c1");

  const [railWidth, setRailWidth] = usePersistentState<number>(RAIL_W_KEY, RAIL_W_DEFAULT);
  const railResizeRef = useDragResize({
    axis: "x",
    edge: "end",
    get: () => railWidth,
    min: 110,
    max: () => Math.round(window.innerWidth * 0.5),
    onResize: setRailWidth,
  });

  // A conductor's persistKey: "global" for the first (back-compat with the old
  // single global chat), namespaced for the rest.
  const persistKeyFor = (id: string) => (id === "c1" ? "global" : `global-${id}`);

  function addConductor() {
    setList((prev) => {
      const id = nextId(prev);
      const c = { id, label: `Conductor ${prev.length + 1}` };
      setActive(id);
      return [...prev, c];
    });
  }

  function closeConductor(id: string) {
    // Drop its persisted transcript + session so a removed conductor doesn't leak.
    try {
      const pk = persistKeyFor(id);
      localStorage.removeItem(`corral.chat.msgs.${pk}`);
      localStorage.removeItem(`corral.chat.sid.${pk}`);
    } catch {
      /* ignore */
    }
    setList((prev) => {
      const next = prev.filter((c) => c.id !== id);
      // Keep at least one conductor around.
      const kept = next.length ? next : [{ id: "c1", label: "Conductor 1" }];
      setActive((cur) => (cur === id ? kept[0].id : cur));
      return kept;
    });
  }

  // Single conductor → no rail, just the chat (the common case; keeps the old
  // look until you actually add a second one).
  if (list.length <= 1) {
    const only = list[0] || { id: "c1", label: "Conductor 1" };
    return (
      <div className="conductors">
        <div className="conductors-solo-head">
          <button type="button" className="work-rail-new" title="Run another global conductor" onClick={addConductor}>
            + New conductor
          </button>
        </div>
        <div className="conductors-view">
          <FirstRunChat persistKey={persistKeyFor(only.id)} onConvMeta={onConvMeta} />
        </div>
      </div>
    );
  }

  return (
    <div className="work-tab conductors">
      <div className="work-rail" style={{ flex: `0 0 ${railWidth}px` }}>
        <div className="work-rail-head">
          <span>Conductors</span>
          <button type="button" className="work-rail-new" title="Run another global conductor" onClick={addConductor}>
            + New
          </button>
        </div>
        {list.map((c, i) => (
          <div key={c.id} className={`work-rail-item${active === c.id ? " active" : ""}`}>
            <button type="button" className="work-rail-btn" onClick={() => setActive(c.id)}>
              <span className="work-rail-label">{c.label || `Conductor ${i + 1}`}</span>
              <span className="work-rail-status">global · host</span>
            </button>
            <button
              type="button"
              className="work-rail-close"
              title="Close this conductor (clears its conversation)"
              onClick={() => closeConductor(c.id)}
            >
              ✕
            </button>
          </div>
        ))}
      </div>

      <div className="work-rail-resize" ref={railResizeRef} title="Drag to resize the list" />

      <div className="work-view">
        {/* All conductors stay mounted (display-toggled) so switching between them
            never drops a live conversation. */}
        {list.map((c) => (
          <div
            key={c.id}
            style={{ display: active === c.id ? "flex" : "none", flex: 1, minHeight: 0, flexDirection: "column" }}
          >
            <FirstRunChat persistKey={persistKeyFor(c.id)} onConvMeta={active === c.id ? onConvMeta : undefined} />
          </div>
        ))}
      </div>
    </div>
  );
}
