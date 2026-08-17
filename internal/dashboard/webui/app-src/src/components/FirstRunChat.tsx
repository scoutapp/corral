import { useCallback, useEffect, useState } from "react";
import { getJSON } from "../api/client";
import { ChatPanel } from "./ChatPanel";

// FirstRunChat gates the GLOBAL chat behind a one-time capability choice. The
// global assistant's power — look only, or act (run corral api) — is a deliberate
// setting stored in the DB, unset until chosen. Until it's set, we show a setup
// card instead of spawning the assistant, so the user decides how much it can do
// on first use. The choice is editable later in Global settings.

type CapResp = { capability: "readonly" | "act" | null; configured: boolean };

export function FirstRunChat() {
  const [state, setState] = useState<CapResp | null>(null);

  const load = useCallback(() => {
    getJSON<CapResp>("/api/chat/capability")
      .then(setState)
      .catch(() => setState({ capability: null, configured: false }));
  }, []);
  useEffect(() => load(), [load]);

  if (!state) return <div className="firstrun-loading">…</div>;
  if (!state.configured) return <FirstRunSetup onDone={load} />;
  return <ChatPanel wsPath={globalChatPath()} />;
}

// globalChatPath adds a light context hint from the current route, so the global
// assistant knows where you are — "this repo" / "this PR" resolves to what you're
// looking at. The backend folds it into the first turn.
function globalChatPath(): string {
  const ctx = pageContext(window.location.pathname);
  return ctx ? `/chat/ws?ctx=${encodeURIComponent(ctx)}` : "/chat/ws";
}

// pageContext returns a short human hint for the current route, or "" if there's
// no useful context (e.g. the projects list).
export function pageContext(path: string): string {
  let m = path.match(/^\/repos\/([^/]+)\/prs\/(\d+)/);
  if (m) return `The user is viewing pull request #${m[2]} of repo ${m[1]}.`;
  m = path.match(/^\/repos\/([^/]+)/);
  if (m) return `The user is viewing repo ${m[1]}.`;
  m = path.match(/^\/p\/([^/]+)/);
  if (m) return `The user is viewing project ${m[1]}.`;
  if (path.startsWith("/logs")) return "The user is viewing the activity logs.";
  if (path.startsWith("/integrations")) return "The user is viewing the MCP integrations.";
  if (path.startsWith("/automations")) return "The user is viewing automations and flows.";
  return "";
}

function FirstRunSetup({ onDone }: { onDone: () => void }) {
  const [choice, setChoice] = useState<"readonly" | "act">("readonly");
  const [saving, setSaving] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  async function save() {
    setSaving(true);
    setErr(null);
    try {
      const r = await fetch("/api/chat/capability", {
        method: "PUT",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ capability: choice }),
      });
      if (!r.ok) throw new Error(`HTTP ${r.status}`);
      onDone();
    } catch (e) {
      setErr(`Couldn't save: ${(e as Error).message}`);
    } finally {
      setSaving(false);
    }
  }

  return (
    <div className="firstrun">
      <h3 className="firstrun-h">Set up the assistant</h3>
      <p className="firstrun-sub">
        Claude here can help across the whole app. Choose how much it can do — you can change this anytime in{" "}
        Global settings.
      </p>

      <label className={`firstrun-opt${choice === "readonly" ? " sel" : ""}`}>
        <input type="radio" name="cap" checked={choice === "readonly"} onChange={() => setChoice("readonly")} />
        <div>
          <div className="firstrun-opt-title">Read-only</div>
          <div className="firstrun-opt-desc">It can look — read logs, list flows, inspect PRs — but not change anything.</div>
        </div>
      </label>

      <label className={`firstrun-opt${choice === "act" ? " sel" : ""}`}>
        <input type="radio" name="cap" checked={choice === "act"} onChange={() => setChoice("act")} />
        <div>
          <div className="firstrun-opt-title">Can act</div>
          <div className="firstrun-opt-desc">
            It can also run things — create issues, start projects, run flows. Changes still require API writes to be
            enabled in Global settings, so this stays under your control.
          </div>
        </div>
      </label>

      {err && <div className="auto-msg err">{err}</div>}

      <button type="button" className="auto-btn firstrun-save" disabled={saving} onClick={save}>
        {saving ? "Saving…" : "Start chatting"}
      </button>
    </div>
  );
}
