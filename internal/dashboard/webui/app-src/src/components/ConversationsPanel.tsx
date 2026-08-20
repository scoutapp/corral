import { useCallback, useEffect, useState } from "react";
import { getJSON, postJSON } from "../api/client";

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
  // Which rows are expanded. A Set (not a single "active") so the list stays
  // put and you can open a conversation inline — like a gutter — and keep
  // scrolling to the ones below it. No back button, no view swap.
  const [expanded, setExpanded] = useState<Set<number>>(new Set());
  const toggle = useCallback((id: number) => {
    setExpanded((prev) => {
      const next = new Set(prev);
      next.has(id) ? next.delete(id) : next.add(id);
      return next;
    });
  }, []);

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
          {items.map((c) => {
            const open = expanded.has(c.id);
            return (
              <li key={c.id} className={`conv-row${open ? " open" : ""}`}>
                <button
                  type="button"
                  className="conv-head"
                  aria-expanded={open}
                  onClick={() => toggle(c.id)}
                >
                  <span className={`conv-caret${open ? " open" : ""}`} aria-hidden>
                    ▸
                  </span>
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
                {open && <ConversationDetail conv={c} />}
              </li>
            );
          })}
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
// search. Tool calls are rendered distinctly from text. Rendered inline inside
// its list row (the gutter) — it has no back button; collapsing is the row's
// own disclosure toggle.
function ConversationDetail({ conv }: { conv: Conversation }) {
  const [msgs, setMsgs] = useState<Message[] | null>(null);
  const [q, setQ] = useState("");
  const [err, setErr] = useState<string | null>(null);
  const [chain, setChain] = useState<Conversation[] | null>(null);
  const [chainOpen, setChainOpen] = useState(false);
  const [analyzeMsg, setAnalyzeMsg] = useState<string | null>(null);

  const viewChain = () => {
    setChainOpen((v) => !v);
    if (chain === null) {
      getJSON<{ conversations: Conversation[] }>(`/api/conversations/${conv.id}/chain`)
        .then((d) => setChain(d.conversations || []))
        .catch((e) => setErr((e as Error).message));
    }
  };

  const analyze = () => {
    setAnalyzeMsg("Starting analysis…");
    postJSON<{ jobId: string }>("/api/conversations/analyze", { conversationId: conv.id })
      .then(() => {
        setAnalyzeMsg("Analyzing — see the Work tab (⌘K)");
        window.dispatchEvent(new CustomEvent("corral:open-work"));
      })
      .catch((e) => setAnalyzeMsg((e as Error).message));
  };

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
        <span className="conv-detail-title">{conv.title || `Conversation #${conv.id}`}</span>
        {conv.parentConversationId ? (
          <span className="conv-chain-hint" title="This conversation was spawned by another">
            ⤴ from #{conv.parentConversationId}
          </span>
        ) : null}
      </div>

      <div className="conv-detail-actions">
        <button type="button" className="auto-btn" onClick={viewChain}>
          {chainOpen ? "Hide chain" : "View chain"}
        </button>
        <button type="button" className="auto-btn" onClick={analyze} title="Spawn a Claude to analyze this conversation (runs in the Work tab; itself captured)">
          ✦ Analyze with AI
        </button>
        {analyzeMsg && <span className="conv-analyze-msg">{analyzeMsg}</span>}
      </div>

      {chainOpen && (
        <div className="conv-chain">
          {chain === null ? (
            <p className="tab-note">Loading chain…</p>
          ) : chain.length <= 1 ? (
            <p className="tab-note">This conversation isn't linked to any others.</p>
          ) : (
            <ul className="conv-chain-list">
              {chain.map((c) => (
                <li key={c.id} className={`conv-chain-item${c.id === conv.id ? " current" : ""}`}>
                  <span className={`conv-origin-chip origin-${c.originKind}`}>{originLabel(c.originKind)}</span>
                  <span className="conv-chain-title">{c.title || `Conversation #${c.id}`}</span>
                  {c.id === conv.id && <span className="conv-chain-you">← this one</span>}
                  <span className="conv-chain-count">{c.messageCount} msg</span>
                </li>
              ))}
            </ul>
          )}
        </div>
      )}

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
