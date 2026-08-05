// Placeholder — full config editor (live+restart zones, creds, SSH keys) lands
// in Commit 3.
export function ConfigTab({ projectId }: { projectId: string }) {
  return (
    <div id="config-root">
      <p className="muted">Config editor is being ported to React (Commit 3). Project: {projectId}</p>
    </div>
  );
}
