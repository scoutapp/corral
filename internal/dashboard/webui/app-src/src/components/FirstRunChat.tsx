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
  return <ChatPanel wsPath="/chat/ws" />;
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
