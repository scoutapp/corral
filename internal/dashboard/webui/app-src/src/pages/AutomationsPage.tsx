import { useState } from "react";
import { Link } from "../router";
import { useBodyClass } from "../hooks/useBodyClass";
import { AutomationsManager } from "../components/AutomationsManager";
import { ScriptsLibrary } from "../components/ScriptsLibrary";

// Top-level Automations page. Two tabs:
//   Automations — prompts, trigger cards (built-in + your steps), advanced.
//   Scripts     — the saved reusable bash-script library.
// Per-repo overrides live in a repo's Settings tab; everything flows through the
// API-first /api/* control plane.

type Tab = "automations" | "scripts";

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
            className={`tab-btn${tab === "scripts" ? " active" : ""}`}
            onClick={() => setTab("scripts")}
          >
            Scripts
          </button>
          {/* Logs is its own full page (searchable/paginated), so it's a link. */}
          <Link to="/automations/logs" className="tab-btn tab-link">
            Logs
          </Link>
        </div>

        {tab === "automations" ? (
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
        ) : (
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
