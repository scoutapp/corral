import { useCallback, useEffect, useRef, useState } from "react";
import { getJSON, wsURL } from "../api/client";

// PromptsCarousel is a swipeable editor over the full prompt catalog
// (/api/prompts). One card is visible at a time (‹ ● ○ › nav); each card shows
// the prompt's name, a "where it's used" callout, the available {{slots}}, an
// editable textarea seeded with the effective text, and Save / Reset-to-default
// / Build-with-AI. Overrides resolve built-in → global → repo (repo wins);
// editing writes at the current scope (global on the Automations page, repo in a
// repo's Settings tab).

type PromptItem = {
  key: string;
  name: string;
  usedWhen: string;
  slots: string[];
  default: string;
  effective: string;
  source: string; // repo | global | default
  overridden: boolean;
};

export function PromptsCarousel({
  repoId,
  onMsg,
}: {
  repoId?: string;
  onMsg: (m: { text: string; err: boolean }) => void;
}) {
  const scopeQ = repoId ? `?repo=${encodeURIComponent(repoId)}` : "";
  const [prompts, setPrompts] = useState<PromptItem[]>([]);
  const [idx, setIdx] = useState(0);
  const [draft, setDraft] = useState(""); // current textarea content
  const [dirty, setDirty] = useState(false);

  // AI draft (only meaningful for the project-start prompt, but harmless for all).
  const [aiIntent, setAiIntent] = useState("");
  const [aiBusy, setAiBusy] = useState(false);
  const [aiLog, setAiLog] = useState("");
  const wsRef = useRef<WebSocket | null>(null);
  useEffect(() => () => wsRef.current?.close(), []);

  const load = useCallback(() => {
    getJSON<{ prompts: PromptItem[] }>(`/api/prompts${scopeQ}`)
      .then((d) => setPrompts(d.prompts || []))
      .catch((e) => onMsg({ text: (e as Error).message, err: true }));
  }, [scopeQ, onMsg]);

  useEffect(() => {
    load();
  }, [load]);

  const cur = prompts[idx];

  // When the visible card changes (or reloads), reset the editor to its text.
  useEffect(() => {
    if (cur) {
      setDraft(cur.effective);
      setDirty(false);
      setAiLog("");
      setAiIntent("");
    }
  }, [cur?.key, cur?.effective]);

  if (prompts.length === 0 || !cur) return null;

  const go = (delta: number) => {
    setIdx((i) => (i + delta + prompts.length) % prompts.length);
  };

  const save = async () => {
    await fetch(`/api/prompts/${encodeURIComponent(cur.key)}${scopeQ}`, {
      method: "PUT",
      credentials: "same-origin",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ template: draft }),
    })
      .then((r) => r.json())
      .then((item: PromptItem) => {
        setPrompts((ps) => ps.map((p) => (p.key === item.key ? item : p)));
        onMsg({ text: "✓ prompt saved", err: false });
      })
      .catch((e) => onMsg({ text: (e as Error).message, err: true }));
  };

  const reset = async () => {
    await fetch(`/api/prompts/${encodeURIComponent(cur.key)}${scopeQ}`, {
      method: "DELETE",
      credentials: "same-origin",
    })
      .then((r) => r.json())
      .then((item: PromptItem) => {
        setPrompts((ps) => ps.map((p) => (p.key === item.key ? item : p)));
        onMsg({ text: "↺ reset to default", err: false });
      })
      .catch((e) => onMsg({ text: (e as Error).message, err: true }));
  };

  const draftWithAI = () => {
    if (!aiIntent.trim()) return;
    wsRef.current?.close();
    setAiBusy(true);
    setAiLog("");
    const ws = new WebSocket(wsURL("/api/prompts/draft"));
    wsRef.current = ws;
    ws.onopen = () => ws.send(JSON.stringify({ description: aiIntent }));
    ws.onmessage = (ev) => {
      let m: Record<string, unknown>;
      try {
        m = JSON.parse(ev.data);
      } catch {
        return;
      }
      if (m.type === "text") setAiLog((l) => l + (m.text as string));
      else if (m.type === "tool_use") setAiLog((l) => l + `› ${m.tool}\n`);
      else if (m.type === "error") setAiLog((l) => l + `\n⚠ ${(m.text as string) || ""}`);
      else if (m.type === "draft" && m.result) {
        setDraft(m.result as string);
        setDirty(true);
      }
    };
    ws.onclose = () => {
      setAiBusy(false);
      wsRef.current = null;
    };
    ws.onerror = () => setAiLog((l) => l + "\n⚠ draft connection failed");
  };

  return (
    <section className="prompts-section">
      <div className="carousel-head">
        <h3 className="auto-mgr-h" style={{ margin: 0 }}>
          Prompts
        </h3>
        <div className="carousel-nav">
          <button type="button" className="carousel-arrow" onClick={() => go(-1)} aria-label="Previous prompt">
            ‹
          </button>
          <div className="carousel-dots">
            {prompts.map((p, i) => (
              <button
                key={p.key}
                type="button"
                className={`carousel-dot${i === idx ? " active" : ""}${p.overridden ? " modified" : ""}`}
                title={p.name}
                onClick={() => setIdx(i)}
              />
            ))}
          </div>
          <button type="button" className="carousel-arrow" onClick={() => go(1)} aria-label="Next prompt">
            ›
          </button>
        </div>
      </div>

      <div className="prompt-card">
        <div className="prompt-card-head">
          {cur.name}
          <span className="auto-scope">{repoId ? "repo" : "global"}</span>
          {cur.overridden && <span className="modified-pill">modified</span>}
          {!cur.overridden && cur.source !== "default" && (
            <span className="inherited-pill">from {cur.source}</span>
          )}
        </div>

        <p className="prompt-usedwhen">↳ {cur.usedWhen}</p>

        {cur.slots.length > 0 && (
          <div className="prompt-slots">
            <span className="prompt-slots-label">Available:</span>
            {cur.slots.map((s) => (
              <code key={s} className="prompt-slot-chip">{`{{${s}}}`}</code>
            ))}
          </div>
        )}

        <textarea
          className="prompt-textarea"
          rows={8}
          value={draft}
          onChange={(e) => {
            setDraft(e.target.value);
            setDirty(true);
          }}
        />

        <div className="prompt-actions-row">
          <button type="button" className="auto-btn" disabled={!dirty} onClick={save}>
            Save
          </button>
          <button type="button" className="auto-btn" disabled={!cur.overridden} onClick={reset}>
            Reset to default
          </button>
        </div>

        {/* Build with AI — most useful for project-start, offered on every card. */}
        <details className="prompt-ai">
          <summary>
            ✨ Build with AI{" "}
            <span className="ai-warn" title="Runs your host machine's Claude, not the sandbox; read-only">
              host · not sandboxed · read-only
            </span>
          </summary>
          <textarea
            className="prompt-textarea"
            rows={2}
            value={aiIntent}
            onChange={(e) => setAiIntent(e.target.value)}
            placeholder="Describe what this prompt should do…"
          />
          <button type="button" className="auto-btn" disabled={aiBusy || !aiIntent.trim()} onClick={draftWithAI}>
            {aiBusy ? "Drafting…" : "Draft with AI"}
          </button>
          {aiLog && <pre className="split-menu-ai-log">{aiLog}</pre>}
        </details>
      </div>
    </section>
  );
}
