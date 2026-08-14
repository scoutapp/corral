import { Link } from "../router";
import { useBodyClass } from "../hooks/useBodyClass";
import { AutomationsManager } from "../components/AutomationsManager";

// Top-level Automations page: the global action catalog + event hook bindings.
// The "meta" surface where a user turns hard-coded event bodies into their own
// units of work. Per-repo overrides live in a repo's Settings tab; both render
// the shared AutomationsManager (this page in global scope). All data flows
// through the API-first /api/* control plane.
export function AutomationsPage() {
  useBodyClass("console");
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
        <p className="auto-intro">
          Turn events into your own units of work. Actions are reusable steps; hooks bind an action to
          an event (like approving a PR) so it also runs. Built-in behavior always runs first — hooks
          are additive and best-effort. These are <b>global</b> defaults; per-repo overrides live in a
          repo's Settings tab.
        </p>

        <AutomationsManager />

        <section className="auto-section">
          <Link to="/automations/runs" className="auto-runs-link">
            View run history →
          </Link>
        </section>
      </div>
    </>
  );
}
