import { Link } from "../router";

// Placeholder — the full project page (tabs: Files, Diff, Container, Mitm,
// Firewall, Config, plus terminal/host/chat docks) is ported in later commits.
export function ProjectPage({ id }: { id: string }) {
  return (
    <div className="app-shell">
      <div className="content">
        <header className="content-head">
          <h1>Project {id}</h1>
          <Link to="/" className="nav-link">
            ← All projects
          </Link>
        </header>
        <p className="muted">Project view is being ported to React (coming in the next commits).</p>
      </div>
    </div>
  );
}
