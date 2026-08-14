import { useRef, useState } from "react";
import { wsURL } from "../api/client";

// BashStepEditor is a friendlier editor for a "run a script" step: the script
// comes FIRST in a monospace editor (tab-to-indent), the available CORRAL_* env
// vars are one-click chips, "Create with AI" drafts a script from a description,
// and "Test run" executes it (unsaved) and shows stdout/stderr/exit. It emits
// the finished bash spec ({"script": ...}) to the parent.

const ENV_VARS = [
  "CORRAL_EVENT",
  "CORRAL_REPO_ID",
  "CORRAL_PR_NUMBER",
  "CORRAL_PR_URL",
  "CORRAL_PR_TITLE",
  "CORRAL_OWNER_NAME",
  "CORRAL_HEAD_SHA",
];

type TestResult = { status: string; output?: string; error?: string; durationMs?: number };

export function BashStepEditor({
  script,
  onChange,
}: {
  script: string;
  onChange: (script: string) => void;
}) {
  const taRef = useRef<HTMLTextAreaElement | null>(null);

  const [testing, setTesting] = useState(false);
  const [result, setResult] = useState<TestResult | null>(null);

  // AI draft.
  const [aiOpen, setAiOpen] = useState(false);
  const [aiIntent, setAiIntent] = useState("");
  const [aiBusy, setAiBusy] = useState(false);
  const [aiLog, setAiLog] = useState("");
  const wsRef = useRef<WebSocket | null>(null);

  // Insert text at the cursor (for env-var chips), keeping focus.
  const insertAtCursor = (text: string) => {
    const ta = taRef.current;
    if (!ta) {
      onChange(script + text);
      return;
    }
    const start = ta.selectionStart ?? script.length;
    const end = ta.selectionEnd ?? script.length;
    const next = script.slice(0, start) + text + script.slice(end);
    onChange(next);
    requestAnimationFrame(() => {
      ta.focus();
      const pos = start + text.length;
      ta.setSelectionRange(pos, pos);
    });
  };

  // Tab inserts two spaces instead of moving focus (IDE feel).
  const onKeyDown = (e: React.KeyboardEvent<HTMLTextAreaElement>) => {
    if (e.key === "Tab") {
      e.preventDefault();
      insertAtCursor("  ");
    }
  };

  const testRun = async () => {
    setTesting(true);
    setResult(null);
    try {
      const res = await fetch("/api/actions:test", {
        method: "POST",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          kind: "bash",
          spec: JSON.stringify({ script }),
          // Sample context so {{CORRAL_*}} vars have something during a test.
          context: {
            event: "test",
            vars: { pr_number: "0", pr_url: "https://example/pr/0", pr_title: "Test PR", owner_name: "owner/repo" },
          },
        }),
      });
      setResult(await res.json());
    } catch (e) {
      setResult({ status: "error", error: (e as Error).message });
    } finally {
      setTesting(false);
    }
  };

  const draftWithAI = () => {
    if (!aiIntent.trim()) return;
    wsRef.current?.close();
    setAiBusy(true);
    setAiLog("");
    const ws = new WebSocket(wsURL("/api/scripts/draft"));
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
      else if (m.type === "draft" && m.result) onChange(m.result as string);
    };
    ws.onclose = () => {
      setAiBusy(false);
      wsRef.current = null;
    };
    ws.onerror = () => setAiLog((l) => l + "\n⚠ draft connection failed");
  };

  return (
    <div className="bash-editor">
      {/* Script first — the star of the show. */}
      <div className="bash-editor-head">
        <span className="bash-editor-label">Script</span>
        <span className="bash-editor-hint">bash · runs in the sandbox</span>
      </div>
      <textarea
        ref={taRef}
        className="bash-editor-ta"
        rows={10}
        spellCheck={false}
        value={script}
        onChange={(e) => onChange(e.target.value)}
        onKeyDown={onKeyDown}
        placeholder={'#!/usr/bin/env bash\nset -euo pipefail\n\necho "PR $CORRAL_PR_NUMBER approved"'}
      />

      <div className="bash-vars">
        <span className="bash-vars-label">Insert var:</span>
        {ENV_VARS.map((v) => (
          <button key={v} type="button" className="bash-var-chip" onClick={() => insertAtCursor("$" + v)}>
            ${v}
          </button>
        ))}
      </div>

      <div className="bash-editor-actions">
        <button type="button" className="auto-btn" disabled={testing || !script.trim()} onClick={testRun}>
          {testing ? "Running…" : "▶ Test run"}
        </button>
        <button type="button" className="auto-btn" onClick={() => setAiOpen((o) => !o)}>
          ✨ Create with AI
        </button>
      </div>

      {result && (
        <div className={`bash-result ${result.status === "ok" ? "ok" : "err"}`}>
          <div className="bash-result-head">
            {result.status === "ok" ? "✓ exit 0" : "✗ failed"}
            {typeof result.durationMs === "number" && <span className="bash-result-dur">{result.durationMs}ms</span>}
          </div>
          {result.output && <pre className="bash-result-out">{result.output}</pre>}
          {result.error && <pre className="bash-result-out err">{result.error}</pre>}
        </div>
      )}

      {aiOpen && (
        <div className="bash-ai">
          <div className="bash-ai-head">
            <span className="ai-warn" title="Runs your host machine's Claude, not the sandbox; read-only">
              host · not sandboxed · read-only
            </span>
          </div>
          <textarea
            className="bash-editor-ta"
            rows={2}
            value={aiIntent}
            onChange={(e) => setAiIntent(e.target.value)}
            placeholder="Describe the script — e.g. 'post a Slack thumbs-up on the PR and comment the CI link'"
          />
          <button type="button" className="auto-btn" disabled={aiBusy || !aiIntent.trim()} onClick={draftWithAI}>
            {aiBusy ? "Drafting…" : "Draft script"}
          </button>
          {aiLog && <pre className="split-menu-ai-log">{aiLog}</pre>}
        </div>
      )}
    </div>
  );
}
