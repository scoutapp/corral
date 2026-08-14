import { useEffect, useRef, useState } from "react";
import { wsURL } from "../api/client";
import { loadEditor, type EditorHandle } from "../lib/editor";

// BashStepEditor is a friendlier editor for a "run a script" step: the script
// comes FIRST in a real CodeMirror editor with bash syntax highlighting, the
// available CORRAL_* env vars are one-click chips, "Create with AI" drafts a
// script from a description, and "Test run" executes it (unsaved) and shows
// stdout/stderr/exit. It emits the finished bash spec ({"script": ...}) via
// onChange.

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
  // CodeMirror editor with bash highlighting. We mount once and drive changes
  // through its onChange; a ref holds the current doc for chip-insert + submit.
  const hostRef = useRef<HTMLDivElement | null>(null);
  const edRef = useRef<EditorHandle | null>(null);
  const scriptRef = useRef(script);
  const [ready, setReady] = useState(false);

  useEffect(() => {
    let handle: EditorHandle | null = null;
    let cancelled = false;
    loadEditor()
      .then((api) => {
        if (cancelled || !hostRef.current) return;
        handle = api.createEditor({
          parent: hostRef.current,
          doc: scriptRef.current,
          language: "bash",
          onChange: (doc: string) => {
            scriptRef.current = doc;
            onChange(doc);
          },
        });
        edRef.current = handle;
        setReady(true);
      })
      .catch(() => setReady(false)); // fall back to the plain textarea below
    return () => {
      cancelled = true;
      handle?.destroy();
      edRef.current = null;
    };
    // Mount once; script is seeded from the ref.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const [testing, setTesting] = useState(false);
  const [result, setResult] = useState<TestResult | null>(null);

  // AI draft.
  const [aiOpen, setAiOpen] = useState(false);
  const [aiIntent, setAiIntent] = useState("");
  const [aiBusy, setAiBusy] = useState(false);
  const [aiLog, setAiLog] = useState("");
  const wsRef = useRef<WebSocket | null>(null);

  // Replace the whole doc (AI draft / would-be programmatic changes).
  const setDoc = (next: string) => {
    scriptRef.current = next;
    onChange(next);
    edRef.current?.setDoc?.(next);
  };

  // Insert an env var. With CodeMirror we append at the end (simple + reliable);
  // the textarea fallback inserts at the cursor.
  const insertVar = (text: string) => {
    if (edRef.current) {
      setDoc((scriptRef.current || "") + text);
      return;
    }
    setDoc((scriptRef.current || "") + text);
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
          spec: JSON.stringify({ script: scriptRef.current }),
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
      else if (m.type === "draft" && m.result) setDoc(m.result as string);
    };
    ws.onclose = () => {
      setAiBusy(false);
      wsRef.current = null;
    };
    ws.onerror = () => setAiLog((l) => l + "\n⚠ draft connection failed");
  };

  return (
    <div className="bash-editor">
      <div className="bash-editor-head">
        <span className="bash-editor-label">Script</span>
        <span className="bash-editor-hint">bash · runs in the sandbox</span>
      </div>

      {/* CodeMirror mounts here; a textarea fallback shows until it's ready or if
          the bundle fails to load. */}
      <div ref={hostRef} className="bash-editor-cm" />
      {!ready && (
        <textarea
          className="bash-editor-ta"
          rows={10}
          spellCheck={false}
          defaultValue={script}
          onChange={(e) => setDoc(e.target.value)}
          placeholder={'#!/usr/bin/env bash\nset -euo pipefail\n\necho "PR $CORRAL_PR_NUMBER approved"'}
        />
      )}

      <div className="bash-vars">
        <span className="bash-vars-label">Insert var:</span>
        {ENV_VARS.map((v) => (
          <button key={v} type="button" className="bash-var-chip" onClick={() => insertVar("$" + v)}>
            ${v}
          </button>
        ))}
      </div>

      <div className="bash-editor-actions">
        <button type="button" className="auto-btn" disabled={testing} onClick={testRun}>
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
