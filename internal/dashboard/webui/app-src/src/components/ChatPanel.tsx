import { useCallback, useEffect, useRef, useState } from "react";
import { wsURL } from "../api/client";
import { renderMarkdown } from "../lib/markdown";
import { parseLiveViewReady } from "../lib/liveViewReady";
import { parseChatQuestion } from "../lib/chatQuestion";
import { LiveViewReadyCard } from "./LiveViewReadyCard";

// Ask Claude: a host-claude chat over /p/<id>/chat/ws. The user's turn is sent
// as {prompt}; the server streams typed frames (text/tool_use/tool_result/
// result/error/turn_end) rendered as bubbles + tool cards. NOTE: this runs the
// HOST claude (not sandboxed) — the header warning says so, and (via canAct)
// states honestly whether the session is read-only or can also run Bash.

interface Msg {
  role: "user" | "assistant" | "meta";
  html?: string; // for user/assistant bubbles
  text?: string; // meta text, or the raw assistant text (used to detect the Live View block)
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
//
// persistKey, when provided, persists the transcript + Claude session id under
// that localStorage key. On mount the transcript is restored and the connection
// reconnects with ?resume=<id>, so a full page reload continues the same session
// instead of losing it. Omit for ephemeral chats (e.g. per-PR review).
// canAct describes what the chat can actually do, so the header warning is
// HONEST: "readonly" → Read/Grep/Glob only; "act" → also runs Bash (e.g. corral
// api, docker) on the host. Defaults to readonly (the safe description).
export function ChatPanel({
  wsPath,
  getCtx,
  persistKey,
  canAct = false,
  onConvMeta,
}: {
  wsPath: string;
  getCtx?: () => string;
  persistKey?: string;
  canAct?: boolean;
  // Fires once per conversation with its captured id + stable UUID (from the
  // server's conv_meta frame). The host chat header shows the UUID.
  onConvMeta?: (meta: { convId: number; convUuid: string }) => void;
}) {
  const msgsKey = persistKey ? `corral.chat.msgs.${persistKey}` : "";
  const sidKey = persistKey ? `corral.chat.sid.${persistKey}` : "";

  const [msgs, setMsgs] = useState<Msg[]>(() => {
    if (!msgsKey) return [];
    try {
      const raw = localStorage.getItem(msgsKey);
      return raw ? (JSON.parse(raw) as Msg[]) : [];
    } catch {
      return [];
    }
  });
  const [input, setInput] = useState("");
  const [ready, setReady] = useState(false);
  const [busy, setBusy] = useState(false);
  // A follow-up typed while a turn is streaming. Instead of dropping it (the old
  // behavior — Enter did nothing while busy), we queue ONE message and fire it as
  // the next turn when the current one ends. queuedRef mirrors it so the WS
  // onmessage closure (turn_end) can read + dispatch without a reconnect.
  const [queued, setQueued] = useState<string | null>(null);
  const queuedRef = useRef<string | null>(null);
  queuedRef.current = queued;
  // sendPromptRef lets the turn_end handler dispatch the queued message through the
  // same code path submit() uses, without capturing a stale closure.
  const sendPromptRef = useRef<(text: string) => void>(() => {});
  const [reconnect, setReconnect] = useState(0); // bump to force a fresh connection
  const wsRef = useRef<WebSocket | null>(null);
  const logRef = useRef<HTMLDivElement | null>(null);
  // Keep the latest onConvMeta in a ref so the WS closure sees it without adding
  // it to the effect deps (which would needlessly reconnect the socket).
  const onConvMetaRef = useRef(onConvMeta);
  onConvMetaRef.current = onConvMeta;
  // The Claude session id (persisted), passed as ?resume= on (re)connect.
  const sessionIdRef = useRef<string>(persistKey ? localStorage.getItem(sidKey) || "" : "");

  // streaming accumulators for the in-flight assistant turn
  const curText = useRef("");
  const curAssistantIdx = useRef<number | null>(null);
  const lastToolIdx = useRef<number | null>(null);

  const scroll = useCallback(() => {
    requestAnimationFrame(() => {
      if (logRef.current) logRef.current.scrollTop = logRef.current.scrollHeight;
    });
  }, []);

  // Persist the transcript so a reload restores it. Skip transient "meta" rows
  // (disconnect/stopped notices) so they don't pile up across reloads.
  useEffect(() => {
    if (!msgsKey) return;
    try {
      const durable = msgs.filter((m) => m.role !== "meta");
      localStorage.setItem(msgsKey, JSON.stringify(durable));
    } catch {
      /* storage full / disabled — transcript just won't persist */
    }
  }, [msgs, msgsKey]);

  useEffect(() => {
    // Reconnect with the persisted session id so a reload continues the same
    // Claude conversation (server --resumes it).
    const sid = sessionIdRef.current;
    const path = sid ? `${wsPath}${wsPath.includes("?") ? "&" : "?"}resume=${encodeURIComponent(sid)}` : wsPath;
    const ws = new WebSocket(wsURL(path));
    wsRef.current = ws;
    // Set true by the cleanup when we're deliberately tearing down (unmount, or a
    // New-chat/reconnect) — so we don't show a scary "disconnected" notice for an
    // intentional close.
    let closingIntentionally = false;
    ws.onopen = () => setReady(true);
    ws.onclose = () => {
      setReady(false);
      setBusy(false);
      if (!closingIntentionally) {
        setMsgs((m) => [...m, { role: "meta", text: "disconnected — reopen the panel to reconnect", error: true }]);
      }
    };
    ws.onmessage = (ev) => {
      let m: Record<string, unknown>;
      try {
        m = JSON.parse(ev.data);
      } catch {
        return;
      }
      switch (m.type as string) {
        case "session": {
          // Claude's session id — remember it so a reload can resume.
          const sid = (m.sessionId as string) || "";
          if (sid) {
            sessionIdRef.current = sid;
            if (sidKey) {
              try {
                localStorage.setItem(sidKey, sid);
              } catch {
                /* storage full / disabled — resume just won't persist */
              }
            }
          }
          break;
        }
        case "conv_meta": {
          // The conversation's stable public UUID — surface it to the host chat
          // header (sandbox chats don't render it).
          const convUuid = (m.convUuid as string) || "";
          const convId = (m.convId as number) || 0;
          if (convUuid && onConvMetaRef.current) onConvMetaRef.current({ convId, convUuid });
          break;
        }
        case "text": {
          curText.current += (m.text as string) || "";
          setMsgs((prev) => {
            const next = prev.slice();
            if (curAssistantIdx.current == null) {
              next.push({ role: "assistant", html: "" });
              curAssistantIdx.current = next.length - 1;
            }
            next[curAssistantIdx.current] = { role: "assistant", html: renderMarkdown(curText.current), text: curText.current };
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
          // A message queued during this turn now fires as the next turn.
          if (queuedRef.current != null) {
            const next = queuedRef.current;
            setQueued(null);
            queuedRef.current = null;
            // Defer so setBusy(false) has committed before we start the next turn.
            setTimeout(() => sendPromptRef.current(next), 0);
          }
          break;
      }
    };
    return () => {
      closingIntentionally = true;
      try {
        ws.close();
      } catch {
        /* ignore */
      }
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [wsPath, scroll, reconnect]);

  // sendPrompt does the actual send: append the user bubble, ship the frame, and
  // mark busy. Used for a direct send AND for firing a queued message on turn_end.
  const sendPrompt = useCallback(
    (text: string) => {
      if (!wsRef.current || wsRef.current.readyState !== WebSocket.OPEN) return;
      setMsgs((m) => [...m, { role: "user", html: renderMarkdown(text) }]);
      wsRef.current.send(JSON.stringify({ prompt: text, ctx: getCtx?.() || "" }));
      setBusy(true);
      scroll();
    },
    [getCtx, scroll],
  );
  sendPromptRef.current = sendPrompt;

  const submit = () => {
    const text = input.trim();
    if (!ready || !text) return;
    // While a turn is streaming, queue the message (fired on turn_end) instead of
    // dropping it. One queued message; a second send replaces it.
    if (busy) {
      setQueued(text);
      setInput("");
      return;
    }
    sendPrompt(text);
    setInput("");
  };
  const cancel = () => {
    if (ready && busy) wsRef.current?.send(JSON.stringify({ action: "cancel" }));
  };
  // answer sends a picked corral-question option as the next turn. The chips only
  // render when idle (see the render gate), so a direct send is safe.
  const answer = (opt: string) => {
    if (!ready || busy) return;
    sendPrompt(opt);
  };

  // newChat clears the persisted transcript + session id, then bumps the reconnect
  // nonce so the connect effect tears down and reopens WITHOUT a resume id — a
  // fresh Claude session.
  const newChat = () => {
    sessionIdRef.current = "";
    if (sidKey) {
      try {
        localStorage.removeItem(sidKey);
      } catch {
        /* ignore */
      }
    }
    setMsgs([]);
    setQueued(null);
    queuedRef.current = null;
    curText.current = "";
    curAssistantIdx.current = null;
    lastToolIdx.current = null;
    setReconnect((n) => n + 1);
  };

  return (
    <div className={`chat-root${busy ? " busy" : ""}`}>
      <div className="chat-topbar">
        <p className="muted cfg-note chat-warn">
          ⚠ This runs the <strong>host</strong> Claude — it is <strong>not sandboxed</strong>.{" "}
          {canAct ? (
            <>
              It can <strong>act</strong> — Read/Grep/Glob plus <strong>Bash</strong> (runs commands, <code>corral api</code>) on your machine.
            </>
          ) : (
            <>Read-only: Read, Grep, Glob — it can look, not act.</>
          )}
        </p>
        {persistKey && msgs.length > 0 && (
          <button type="button" className="chat-newbtn" onClick={newChat} title="Start a fresh conversation">
            New chat
          </button>
        )}
      </div>
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
          // A worker's "=== LIVE VIEW READY ===" footer renders as a green
          // verified callout; any prose before/after it still renders as markdown.
          const lvr = m.role === "assistant" ? parseLiveViewReady(m.text || "") : null;
          if (lvr) {
            return (
              <div key={i} className={`msg ${m.role}`}>
                <div className="avatar">✳</div>
                <div className="bubble">
                  {lvr.rest && <div dangerouslySetInnerHTML={{ __html: renderMarkdown(lvr.rest) }} />}
                  <LiveViewReadyCard data={lvr} />
                </div>
              </div>
            );
          }
          // A `corral-question` block (only on the LAST assistant message, once the
          // turn has finished streaming) renders as clickable option chips; a click
          // sends that option as the answer. Only the newest one is interactive so
          // stale questions from earlier in the transcript don't re-arm.
          const isLast = i === msgs.length - 1;
          const cq = m.role === "assistant" && isLast && !busy ? parseChatQuestion(m.text || "") : null;
          if (cq) {
            return (
              <div key={i} className={`msg ${m.role}`}>
                <div className="avatar">✳</div>
                <div className="bubble">
                  {cq.rest && <div dangerouslySetInnerHTML={{ __html: renderMarkdown(cq.rest) }} />}
                  <div className="chat-question">
                    <div className="chat-question-q">{cq.question}</div>
                    <div className="chat-question-opts">
                      {cq.options.map((opt, oi) => (
                        <button key={oi} type="button" className="chat-question-opt" onClick={() => answer(opt)}>
                          {opt}
                        </button>
                      ))}
                    </div>
                  </div>
                </div>
              </div>
            );
          }
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
        {queued != null && (
          <div className="msg user chat-queued" title="Sends when the current turn finishes">
            <div className="avatar">Y</div>
            <div className="bubble">
              <span className="chat-queued-tag">queued</span>
              {queued}
              <button type="button" className="chat-queued-x" title="Cancel this queued message" onClick={() => setQueued(null)}>
                ×
              </button>
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
        {/* While busy, Send stays available and queues the message (fired when the
            current turn ends); Stop cancels the in-flight turn. */}
        <button id="send" disabled={!ready || input.trim() === ""} onClick={submit} title={busy ? "Queue this message — sends when the current turn finishes" : "Send"}>
          {busy ? "Queue" : "Send"}
        </button>
        {busy && (
          <button id="stop" onClick={cancel}>
            Stop
          </button>
        )}
      </div>
    </div>
  );
}
