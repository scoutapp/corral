import { useCallback, useEffect, useState } from "react";
import { getJSON } from "../api/client";
import { ChatPanel } from "./ChatPanel";

// FirstRunChat gates the GLOBAL chat behind a one-time setup. Two host-Claude
// permission choices are deliberate settings stored server-side, unset until
// chosen: the chat capability (look only vs. act) and whether the CLI/skill may
// make API writes. Until BOTH are configured we show the setup card instead of
// spawning the assistant — so if either was never decided (e.g. api-writes on an
// install that predates the prompt), the user still gets asked. Editable later in
// Global settings.

type CapResp = { capability: "readonly" | "act" | null; configured: boolean };
type GlobalResp = { api_writes_enabled: boolean; api_writes_configured: boolean };

export function FirstRunChat({
  onConvMeta,
  persistKey = "global",
  onBusyChange,
}: {
  onConvMeta?: (meta: { convId: number; convUuid: string }) => void;
  // localStorage key for this conductor's transcript + session, so multiple
  // global conductors each keep their own conversation. Defaults to "global".
  persistKey?: string;
  // Bubbled up from ChatPanel so a conductor rail can show a working/waiting dot.
  onBusyChange?: (busy: boolean) => void;
} = {}) {
  const [cap, setCap] = useState<CapResp | null>(null);
  const [writes, setWrites] = useState<GlobalResp | null>(null);

  const load = useCallback(() => {
    getJSON<CapResp>("/api/chat/capability")
      .then(setCap)
      .catch(() => setCap({ capability: null, configured: false }));
    getJSON<GlobalResp>("/global/config")
      .then(setWrites)
      .catch(() => setWrites({ api_writes_enabled: false, api_writes_configured: true })); // don't block on a fetch error
  }, []);
  useEffect(() => load(), [load]);

  if (!cap || !writes) return <div className="firstrun-loading">…</div>;
  // Prompt if EITHER choice is still unmade.
  if (!cap.configured || !writes.api_writes_configured) {
    return <FirstRunSetup cap={cap} writes={writes} onDone={load} />;
  }
  // A STABLE WS URL: the page-context hint is sent per-message (getCtx), not baked
  // into the URL — so moving between pages doesn't reconnect and drop the session.
  return (
    <ChatPanel
      wsPath="/chat/ws"
      getCtx={() => pageContext(window.location.pathname)}
      persistKey={persistKey}
      canAct={cap.capability === "act"}
      onConvMeta={onConvMeta}
      onBusyChange={onBusyChange}
    />
  );
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

function FirstRunSetup({ cap, writes, onDone }: { cap: CapResp; writes: GlobalResp; onDone: () => void }) {
  // Which choices this card is responsible for. If one was already made (e.g. the
  // chat capability on an older install), we only ask about the unmade one.
  const askCap = !cap.configured;
  const askWrites = !writes.api_writes_configured;

  const [choice, setChoice] = useState<"readonly" | "act">(cap.capability || "readonly");
  const [allowWrites, setAllowWrites] = useState<boolean>(writes.api_writes_enabled);
  const [saving, setSaving] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  async function send(path: string, method: string, body: unknown) {
    const r = await fetch(path, {
      method,
      credentials: "same-origin",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
    });
    if (!r.ok) throw new Error(`HTTP ${r.status}`);
  }

  async function save() {
    setSaving(true);
    setErr(null);
    try {
      // Only write the settings this card actually asked about, so we never
      // overwrite a choice the user already made. Capability is a PUT; global
      // settings apply is a POST.
      if (askCap) await send("/api/chat/capability", "PUT", { capability: choice });
      if (askWrites) await send("/global/apply", "POST", { api_writes_enabled: allowWrites });
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

      {askCap && (
        <>
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
                It can also run things — create issues, start projects, run flows. Doing so needs API writes (below).
              </div>
            </div>
          </label>
        </>
      )}

      {askWrites && (
        <label className={`firstrun-opt${allowWrites ? " sel" : ""}`}>
          <input type="checkbox" checked={allowWrites} onChange={(e) => setAllowWrites(e.target.checked)} />
          <div>
            <div className="firstrun-opt-title">Allow API writes</div>
            <div className="firstrun-opt-desc">
              Let the <code>corral api</code> CLI and this assistant make changes — create issues, start projects, run
              flows, create skills &amp; scripts. Leave off to keep them read-only. You stay in control; change it
              anytime in Global settings.
            </div>
          </div>
        </label>
      )}

      {err && <div className="auto-msg err">{err}</div>}

      <button type="button" className="auto-btn firstrun-save" disabled={saving} onClick={save}>
        {saving ? "Saving…" : "Start chatting"}
      </button>
    </div>
  );
}
