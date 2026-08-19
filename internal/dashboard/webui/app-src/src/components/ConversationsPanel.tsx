import { useCallback, useEffect, useState } from "react";
import { getJSON } from "../api/client";

// ConversationsPanel lists captured Claude conversations with filters + deep
// (full-text) search, and drills into one conversation's messages with an
// in-conversation search. Reused by the Logs page "Conversations" tab and the
// sandbox project page (pass projectId to scope it there).

type Conversation = {
  id: number;
  originKind: string;
  originId?: string;
  projectId?: string;
  projectLabel?: string;
  repoId?: string;
  prNumber?: number;
  traceId?: string;
  parentConversationId?: number;
  title?: string;
  firstPrompt?: string;
  model?: string;
  status: string;
  messageCount: number;
  createdAt: string;
};

type Message = {
  id: number;
  seq: number;
  role: string;
  type: string;
  text?: string;
  toolName?: string;
  toolInput?: string;
  toolResult?: string;
  isError?: boolean;
};

// originLabel gives a short human name + tone for an origin kind.
const ORIGIN_LABEL: Record<string, string> = {
  "global-chat": "Global chat",
  "project-chat": "Project chat",
  "pr-review-chat": "PR review chat",
  merge: "Merge",
  worker: "Worker",
  "issue-draft": "Issue draft",
  "prompt-draft": "Prompt draft",
  "script-draft": "Script draft",
  analysis: "Analysis",
  "log-analysis": "Log analysis",
  sandbox: "Sandbox",
};
function originLabel(k: string): string {
  return ORIGIN_LABEL[k] || k;
}

export function ConversationsPanel({ projectId }: { projectId?: string }) {
  const [items, setItems] = useState<Conversation[] | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [query, setQuery] = useState("");
  const [origin, setOrigin] = useState("");
  const [origins, setOrigins] = useState<string[]>([]);
  const [active, setActive] = useState<Conversation | null>(null);

  // Load the filter facets once.
  useEffect(() => {
    getJSON<{ origins: string[] }>("/api/conversations/facets")
      .then((d) => setOrigins(d.origins || []))
      .catch(() => {});
  }, []);

  const load = useCallback(() => {
    const p = new URLSearchParams();
    if (projectId) p.set("project", projectId);
    if (origin) p.set("origin", origin);
    if (query.trim()) p.set("q", query.trim());
    getJSON<{ conversations: Conversation[] }>(`/api/conversations?${p.toString()}`)
      .then((d) => {
        setItems(d.conversations || []);
        setErr(null);
      })
      .catch((e) => setErr((e as Error).message));
  }, [projectId, origin, query]);

  // Debounce search / filter changes.
  useEffect(() => {
    const t = setTimeout(load, 250);
    return () => clearTimeout(t);
  }, [load]);

  if (active) {
    return <ConversationDetail conv={active} onBack={() => setActive(null)} />;
  }

  return (
    <div className="conv-panel">
      <div className="conv-toolbar">
        <input
          className="auto-input conv-search"
          placeholder="🔍 search all conversations (messages + tool calls)…"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
        />
        <select className="cfg-select conv-origin" value={origin} onChange={(e) => setOrigin(e.target.value)}>
          <option value="">All origins</option>
          {origins.map((o) => (
            <option key={o} value={o}>
              {originLabel(o)}
            </option>
          ))}
        </select>
      </div>

      {err && <p className="tab-note err">Failed to load conversations: {err}</p>}
      {items === null ? (
        <p className="tab-note">Loading conversations…</p>
      ) : items.length === 0 ? (
        <p className="tab-note">
          {query.trim() || origin
            ? "No conversations match."
            : "No conversations captured yet. They appear here as Claude works across the app."}
        </p>
      ) : (
        <ul className="conv-list">
          {items.map((c) => (
            <li key={c.id} className="conv-row">
              <button type="button" className="conv-head" onClick={() => setActive(c)}>
                <span className={`conv-origin-chip origin-${c.originKind}`}>{originLabel(c.originKind)}</span>
                <span className="conv-title">{c.title || c.firstPrompt || `Conversation #${c.id}`}</span>
                <ConvStatusDot status={c.status} />
              </button>
              <div className="conv-byline">
                {c.projectLabel && <span>{c.projectLabel}</span>}
                {c.prNumber ? <span> · PR #{c.prNumber}</span> : null}
                <span> · {c.messageCount} msg</span>
                {c.model && <span> · {c.model}</span>}
                <span> · {c.createdAt}</span>
              </div>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

function ConvStatusDot({ status }: { status: string }) {
  const cls =
    status === "running" || status === "idle"
      ? "running"
      : status === "failed" || status === "interrupted" || status === "canceled"
        ? "err"
        : "done";
  return <span className={`conv-status-dot ${cls}`} title={status} />;
}

// ConversationDetail shows one conversation's messages with in-conversation
// search. Tool calls are rendered distinctly from text.
function ConversationDetail({ conv, onBack }: { conv: Conversation; onBack: () => void }) {
  const [msgs, setMsgs] = useState<Message[] | null>(null);
  const [q, setQ] = useState("");
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    const p = new URLSearchParams();
    if (q.trim()) p.set("q", q.trim());
    const t = setTimeout(() => {
      getJSON<{ messages: Message[] }>(`/api/conversations/${conv.id}/messages?${p.toString()}`)
        .then((d) => {
          setMsgs(d.messages || []);
          setErr(null);
        })
        .catch((e) => setErr((e as Error).message));
    }, 200);
    return () => clearTimeout(t);
  }, [conv.id, q]);

  return (
    <div className="conv-detail">
      <div className="conv-detail-head">
        <button type="button" className="auto-btn link" onClick={onBack}>
          ← conversations
        </button>
        <span className={`conv-origin-chip origin-${conv.originKind}`}>{originLabel(conv.originKind)}</span>
        <span className="conv-detail-title">{conv.title || `Conversation #${conv.id}`}</span>
        {conv.parentConversationId ? (
          <span className="conv-chain-hint" title="This conversation was spawned by another">
            ⤴ from #{conv.parentConversationId}
          </span>
        ) : null}
      </div>
      <input
        className="auto-input conv-search"
        placeholder="🔍 search within this conversation…"
        value={q}
        onChange={(e) => setQ(e.target.value)}
      />
      {err && <p className="tab-note err">{err}</p>}
      {msgs === null ? (
        <p className="tab-note">Loading…</p>
      ) : msgs.length === 0 ? (
        <p className="tab-note">{q.trim() ? "No messages match." : "No messages."}</p>
      ) : (
        <ul className="conv-msgs">
          {msgs.map((m) => (
            <li key={m.id} className={`conv-msg role-${m.role}${m.isError ? " err" : ""}`}>
              <span className="conv-msg-role">{m.role}</span>
              {m.type === "tool_use" ? (
                <span className="conv-msg-tool">
                  🔧 <b>{m.toolName}</b> <code>{m.toolInput}</code>
                </span>
              ) : m.type === "tool_result" ? (
                <span className="conv-msg-toolresult">↳ {m.toolResult}</span>
              ) : (
                <span className="conv-msg-text">{m.text}</span>
              )}
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
