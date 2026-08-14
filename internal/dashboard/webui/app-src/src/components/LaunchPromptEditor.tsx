import { useEffect, useRef, useState } from "react";
import { getJSON, wsURL } from "../api/client";

// LaunchPromptEditor shows the project-start prompt that WILL be sent when a
// project launches, resolved built-in → global-saved → repo-override (labeled by
// source), editable for THIS launch, with a "Build with AI" drafter. It doesn't
// change the saved prompt — the parent modal sends whatever the user leaves here
// as the launch prompt. Used in the New-project / issue Advanced sections.
//
// promptKey is "project.start" (plain) or "project.issue". repoId scopes the
// resolution (so a repo override shows). value/onChange let the parent capture
// the (possibly edited) prompt to send.

type PromptItem = { key: string; effective: string; source: string };

// renderVars fills {{name}} placeholders (mirrors the Go renderer; unknown vars
// blank). Applied when seeding so the user sees/sends the FILLED prompt — the
// populate-prompt path doesn't render slots server-side.
function renderVars(tmpl: string, vars: Record<string, string>): string {
  return tmpl.replace(/\{\{\s*([a-zA-Z_][a-zA-Z0-9_]*)\s*\}\}/g, (_, n) => vars[n] ?? "");
}

export function LaunchPromptEditor({
  promptKey,
  repoId,
  value,
  onChange,
  onDirty,
  vars,
}: {
  promptKey: string;
  repoId?: string;
  value: string;
  onChange: (v: string) => void;
  onDirty?: (dirty: boolean) => void; // fired true once the user edits the seeded text
  vars?: Record<string, string>; // slot values to fill when seeding (e.g. issue number/title)
}) {
  const [source, setSource] = useState<string>("default");
  const [loaded, setLoaded] = useState(false);

  // AI draft.
  const [aiOpen, setAiOpen] = useState(false);
  const [aiIntent, setAiIntent] = useState("");
  const [aiBusy, setAiBusy] = useState(false);
  const [aiLog, setAiLog] = useState("");
  const wsRef = useRef<WebSocket | null>(null);
  useEffect(() => () => wsRef.current?.close(), []);

  // Resolve the effective prompt once, seeding the editor (only if the parent
  // hasn't already got a value — so re-opening Advanced doesn't clobber edits).
  useEffect(() => {
    const q = repoId ? `?repo=${encodeURIComponent(repoId)}` : "";
    getJSON<{ prompts: PromptItem[] }>(`/api/prompts${q}`)
      .then((d) => {
        const p = (d.prompts || []).find((x) => x.key === promptKey);
        if (p) {
          setSource(p.source);
          if (!value) onChange(vars ? renderVars(p.effective, vars) : p.effective);
        }
        setLoaded(true);
      })
      .catch(() => setLoaded(true));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [promptKey, repoId]);

  const draftWithAI = () => {
    if (!aiIntent.trim()) return;
    wsRef.current?.close();
    setAiBusy(true);
    setAiLog("");
    const q = repoId ? `?repoId=${encodeURIComponent(repoId)}` : "";
    const ws = new WebSocket(wsURL(`/api/prompts/draft${q}`));
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
        onChange(m.result as string);
        onDirty?.(true);
      }
    };
    ws.onclose = () => {
      setAiBusy(false);
      wsRef.current = null;
    };
    ws.onerror = () => setAiLog((l) => l + "\n⚠ draft connection failed");
  };

  if (!loaded) return null;

  return (
    <div className="launch-prompt">
      <div className="launch-prompt-head">
        <span>Project-start prompt</span>
        <span className="launch-prompt-src" title={`Inherited from the ${source} prompt`}>
          from: {source}
        </span>
      </div>
      <p className="auto-hint" style={{ margin: "0 0 0.35rem" }}>
        Sent to Claude when the project starts. Edited here it applies to this launch only — change the
        saved one under Automations → Prompts.
      </p>
      <textarea
        className="prompt-textarea"
        rows={5}
        value={value}
        onChange={(e) => {
          onChange(e.target.value);
          onDirty?.(true);
        }}
        placeholder="You're working in {{repo}} on branch {{branch}}…"
      />
      <div className="auto-row" style={{ marginTop: "0.3rem" }}>
        <button type="button" className="auto-btn" onClick={() => setAiOpen((o) => !o)}>
          ✨ Build with AI
        </button>
      </div>
      {aiOpen && (
        <div className="bash-ai">
          <div className="bash-ai-head">
            <span className="ai-warn" title="Runs your host machine's Claude, not the sandbox; read-only">
              host · not sandboxed · read-only
            </span>
          </div>
          <textarea
            className="prompt-textarea"
            rows={2}
            value={aiIntent}
            onChange={(e) => setAiIntent(e.target.value)}
            placeholder="Describe what the prompt should do…"
          />
          <button type="button" className="auto-btn" disabled={aiBusy || !aiIntent.trim()} onClick={draftWithAI}>
            {aiBusy ? "Drafting…" : "Draft with AI"}
          </button>
          {aiLog && <pre className="split-menu-ai-log">{aiLog}</pre>}
        </div>
      )}
    </div>
  );
}
