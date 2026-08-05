// Placeholder — full mitm flow table (merged decrypted + direct-dialed rows,
// live "Monitor this host") lands in Commit 3.
export function MitmTab({ projectId, mitmUp }: { projectId: string; mitmUp: boolean }) {
  if (!mitmUp) return <p className="empty">Credential proxy isn't running for this project.</p>;
  return <p className="muted">Mitm flow table is being ported to React (Commit 3). Project: {projectId}</p>;
}
