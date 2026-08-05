import { Link } from "../router";

// Placeholder — the global settings page (creds, SSH keys, populate flow) is
// ported in a later commit.
export function GlobalPage() {
  return (
    <div className="app-shell">
      <div className="content">
        <header className="content-head">
          <h1>Global settings</h1>
          <Link to="/" className="nav-link">
            ← All projects
          </Link>
        </header>
        <p className="muted">Global settings are being ported to React (coming in a later commit).</p>
      </div>
    </div>
  );
}
