import { useState } from "react";
import { Link } from "../router";
import { useBodyClass } from "../hooks/useBodyClass";
import { AutomationsManager } from "../components/AutomationsManager";
import { ScriptsLibrary } from "../components/ScriptsLibrary";
import { FlowsManager } from "../components/FlowsManager";

// Top-level Automations page. Three tabs:
//   Automations — prompts, trigger cards (built-in + your steps), advanced.
//   Flows       — compose multi-step flows, schedule + run them.
//   Scripts     — the saved reusable bash-script library.
// Per-repo overrides live in a repo's Settings tab; everything flows through the
// API-first /api/* control plane.

type Tab = "automations" | "flows" | "scripts";

export function AutomationsPage() {
  useBodyClass("console");
  const [tab, setTab] = useState<Tab>("automations");

  return (
    <>
      <header className="console-header">
        <div className="brand">
          <Link to="/" className="back">
            ← All repos
          </Link>
          <span className="brand-name">Automations</span>
          <Link to="/global" className="brand-sub">
            global settings →
          </Link>
        </div>
      </header>

      <div className="auto-page">
        <div className="tabs" style={{ marginBottom: "1rem" }}>
          <button
            type="button"
            className={`tab-btn${tab === "automations" ? " active" : ""}`}
            onClick={() => setTab("automations")}
          >
            Automations
          </button>
          <button
            type="button"
            className={`tab-btn${tab === "flows" ? " active" : ""}`}
            onClick={() => setTab("flows")}
          >
            Flows
          </button>
          <button
            type="button"
            className={`tab-btn${tab === "scripts" ? " active" : ""}`}
            onClick={() => setTab("scripts")}
          >
            Scripts
          </button>
        </div>

        {tab === "automations" && (
          <>
            <p className="auto-intro">
              Turn events into your own units of work. Each card is something Corral already does — add
              your own steps to run alongside it. These are <b>global</b> defaults; per-repo overrides
              live in a repo's Settings tab.
            </p>
            <AutomationsManager />
            <section className="auto-section">
              <Link to="/automations/runs" className="auto-runs-link">
                View run history →
              </Link>
            </section>
          </>
        )}

        {tab === "flows" && (
          <>
            <p className="auto-intro">
              Wire actions into a multi-step flow. Each step hands its result to the next as{" "}
              <code>{"{{steps.<key>.output}}"}</code>; a step can wait for others with <b>after</b>. Run a
              flow now, or put it on a schedule.
            </p>
            <FlowsManager />
          </>
        )}

        {tab === "scripts" && (
          <>
            <h2 className="auto-mgr-h" style={{ marginTop: 0 }}>
              Saved scripts
            </h2>
            <ScriptsLibrary />
          </>
        )}
      </div>
    </>
  );
}
