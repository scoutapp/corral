import { useCallback, useEffect, useRef, useState } from "react";
import { wsURL } from "../api/client";
import { renderMarkdown } from "../lib/markdown";

// Ask Claude: a host-claude chat over /p/<id>/chat/ws. The user's turn is sent
// as {prompt}; the server streams typed frames (text/tool_use/tool_result/
// result/error/turn_end) rendered as bubbles + tool cards. Read-only tools by
// default. Port of chat.js. NOTE: this runs the HOST claude (not sandboxed) —
// the panel says so explicitly.

interface Msg {
  role: "user" | "assistant" | "meta";
  html?: string; // for user/assistant bubbles
  text?: string; // for meta
  error?: boolean;
  tool?: { name: string; input: string; result?: string };
}

function summarizeInput(inputJson: string): string {
  if (!inputJson) return "";
  let o: Record<string, unknown>;
  try {
    o = JSON.parse(inputJson);
  } catch {
    return "";
  }
  for (const k of ["file_path", "path", "pattern", "command", "query", "url"]) {
    if (o[k] != null) return String(o[k]);
  }
  const s = inputJson.replace(/\s+/g, " ");
  return s.length > 80 ? s.slice(0, 79) + "…" : s;
}
function prettyJson(s: string): string {
  try {
    return JSON.stringify(JSON.parse(s), null, 2);
  } catch {
    return s;
  }
}

// ChatPanel is the host-Claude chat used by the project drawer and the PR-review
// drawer. It's WS-URL-agnostic: pass the chat WebSocket path (e.g.
// `/p/<id>/chat/ws?tools=…` or `/prs/<id>/chat/ws`). The server streams the same
// typed frames (text/tool_use/tool_result/result/error/turn_end) in both cases.
// getCtx, when provided, returns a per-message page-context hint sent with each
// prompt (so the WS URL stays stable across navigation — no reconnect). Called at
// SEND time so the hint reflects the page the user is on when they send.
export function ChatPanel({ wsPath, getCtx }: { wsPath: string; getCtx?: () => string }) {
  const [msgs, setMsgs] = useState<Msg[]>([]);
  const [input, setInput] = useState("");
  const [ready, setReady] = useState(false);
  const [busy, setBusy] = useState(false);
  const wsRef = useRef<WebSocket | null>(null);
  const logRef = useRef<HTMLDivElement | null>(null);

  // streaming accumulators for the in-flight assistant turn
  const curText = useRef("");
  const curAssistantIdx = useRef<number | null>(null);
  const lastToolIdx = useRef<number | null>(null);

  const scroll = useCallback(() => {
    requestAnimationFrame(() => {
      if (logRef.current) logRef.current.scrollTop = logRef.current.scrollHeight;
    });
  }, []);

  useEffect(() => {
    const ws = new WebSocket(wsURL(wsPath));
    wsRef.current = ws;
    ws.onopen = () => setReady(true);
    ws.onclose = () => {
      setReady(false);
      setBusy(false);
      setMsgs((m) => [...m, { role: "meta", text: "disconnected — reopen the panel to reconnect", error: true }]);
    };
    ws.onmessage = (ev) => {
      let m: Record<string, unknown>;
      try {
        m = JSON.parse(ev.data);
      } catch {
        return;
      }
      switch (m.type as string) {
        case "text": {
          curText.current += (m.text as string) || "";
          setMsgs((prev) => {
            const next = prev.slice();
            if (curAssistantIdx.current == null) {
              next.push({ role: "assistant", html: "" });
              curAssistantIdx.current = next.length - 1;
            }
            next[curAssistantIdx.current] = { role: "assistant", html: renderMarkdown(curText.current) };
            return next;
          });
          scroll();
          break;
        }
        case "tool_use": {
          curAssistantIdx.current = null;
          curText.current = "";
          setMsgs((prev) => {
            const next = [...prev, { role: "assistant" as const, tool: { name: (m.tool as string) || "tool", input: (m.input as string) || "" } }];
            lastToolIdx.current = next.length - 1;
            return next;
          });
          scroll();
          break;
        }
        case "tool_result": {
          setMsgs((prev) => {
            if (lastToolIdx.current == null) return prev;
            const next = prev.slice();
            const t = next[lastToolIdx.current];
            if (t?.tool) next[lastToolIdx.current] = { ...t, tool: { ...t.tool, result: (m.result as string) || "" } };
            return next;
          });
          scroll();
          break;
        }
        case "result": {
          // Show the model on a completed turn, but not the per-turn cost — this
          // runs the user's local Claude Code (their subscription), so a dollar
          // figure is noise here.
          if (m.isError) setMsgs((x) => [...x, { role: "meta", text: "Claude reported an error for this turn.", error: true }]);
          else if (m.model) setMsgs((x) => [...x, { role: "meta", text: m.model as string }]);
          break;
        }
        case "error":
          setMsgs((x) => [...x, { role: "meta", text: (m.text as string) || "error", error: true }]);
          break;
        case "canceled":
          setMsgs((x) => [...x, { role: "meta", text: "stopped" }]);
          break;
        case "turn_end":
          setBusy(false);
          curAssistantIdx.current = null;
          curText.current = "";
          lastToolIdx.current = null;
          break;
      }
    };
    return () => {
      try {
        ws.close();
      } catch {
        /* ignore */
      }
    };
  }, [wsPath, scroll]);

  const submit = () => {
    const text = input.trim();
    if (!ready || busy || !text) return;
    setMsgs((m) => [...m, { role: "user", html: renderMarkdown(text) }]);
    wsRef.current?.send(JSON.stringify({ prompt: text, ctx: getCtx?.() || "" }));
    setInput("");
    setBusy(true);
    scroll();
  };
  const cancel = () => {
    if (ready && busy) wsRef.current?.send(JSON.stringify({ action: "cancel" }));
  };

  return (
    <div className={`chat-root${busy ? " busy" : ""}`}>
      <p className="muted cfg-note" style={{ padding: "0.4rem 0.6rem", borderBottom: "1px solid var(--border, #222)" }}>
        ⚠ This runs the <strong>host</strong> Claude — it is <strong>not sandboxed</strong>. Read-only tools (Read, Grep, Glob) by default.
      </p>
      <div className="chat-log" id="log" ref={logRef}>
        {msgs.map((m, i) => {
          if (m.role === "meta")
            return (
              <div key={i} className={`meta${m.error ? " error" : ""}`}>
                {m.text}
              </div>
            );
          if (m.tool)
            return (
              <div key={i} className="tool">
                <details>
                  <summary>
                    <span className="tool-name">{m.tool.name}</span> <span className="tool-arg">{summarizeInput(m.tool.input)}</span>
                  </summary>
                  <div className="tool-body">
                    {m.tool.input && (
                      <>
                        <div className="label">input</div>
                        <pre>{prettyJson(m.tool.input)}</pre>
                      </>
                    )}
                    {m.tool.result != null && (
                      <>
                        <div className="label">result</div>
                        <pre>{m.tool.result}</pre>
                      </>
                    )}
                  </div>
                </details>
              </div>
            );
          return (
            <div key={i} className={`msg ${m.role}`}>
              <div className="avatar">{m.role === "assistant" ? "✳" : "Y"}</div>
              <div className="bubble" dangerouslySetInnerHTML={{ __html: m.html || "" }} />
            </div>
          );
        })}
        {busy && curAssistantIdx.current == null && (
          <div className="msg assistant">
            <div className="avatar">✳</div>
            <div className="bubble">
              <span className="typing">Claude is thinking…</span>
            </div>
          </div>
        )}
      </div>
      <div className="chat-composer">
        <textarea
          id="input"
          placeholder="Ask Claude about this project…"
          value={input}
          onChange={(e) => setInput(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter" && !e.shiftKey) {
              e.preventDefault();
              submit();
            }
          }}
          rows={1}
          style={{ maxHeight: 160 }}
        />
        {busy ? (
          <button id="stop" onClick={cancel}>
            Stop
          </button>
        ) : (
          <button id="send" disabled={!ready || input.trim() === ""} onClick={submit}>
            Send
          </button>
        )}
      </div>
    </div>
  );
}
