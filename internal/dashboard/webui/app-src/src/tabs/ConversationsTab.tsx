import { ConversationsPanel } from "../components/ConversationsPanel";

// ConversationsTab is the project page's captured-conversation view: the shared
// ConversationsPanel scoped to this project. It shows host-side conversations
// tied to the project (its "Ask Claude" chats, drafts, analyses) plus the
// sandbox's OWN Claude sessions (captured by the host-pull tailer).
export function ConversationsTab({ projectId }: { projectId: string }) {
  return (
    <div className="conv-tab">
      <div className="conv-tab-note muted">
        Captured Claude conversations for this project — host-side chats and the
        sandbox's own Claude sessions. Searchable; kept per your retention setting
        even after the project is removed.
      </div>
      <ConversationsPanel projectId={projectId} />
    </div>
  );
}
