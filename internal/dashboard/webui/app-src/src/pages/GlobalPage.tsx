import { useCallback, useEffect, useState } from "react";
import { Link } from "../router";
import { getJSON, postJSON, postRaw } from "../api/client";
import type { CredSet, GlobalEdit, GlobalView } from "../api/types";
import { XtermPane } from "../components/XtermPane";
import { useBodyClass } from "../hooks/useBodyClass";
import { useDnd } from "../hooks/useDnd";

// Global settings: shared credentials (masked), cross-project defaults
// (monitor-list, mitm-ports) new projects inherit, default SSH keys loaded by
// every project's scoped agent, and a Populate button that runs the interactive
// `claude setup-token` flow in a bridged terminal. Port of global.js.

function sshBasename(p: string): string {
  return p.replace(/^.*\//, "");
}

// Hour options (0–24) for the Do Not Disturb window selects.
const HOURS = Array.from({ length: 25 }, (_, h) => h);
function fmtHour(h: number): string {
  if (h === 0 || h === 24) return "12am";
  if (h === 12) return "12pm";
  return h < 12 ? `${h}am` : `${h - 12}pm`;
}

export function GlobalPage() {
  useBodyClass("console");
  const dnd = useDnd();
  const [g, setG] = useState<GlobalView | null>(null);
  const [msg, setMsg] = useState<{ text: string; err: boolean } | null>(null);
  const [monitor, setMonitor] = useState("");
  const [ports, setPorts] = useState("");
  const [newCreds, setNewCreds] = useState<CredSet[]>([]);
  const [removedCreds, setRemovedCreds] = useState<Record<string, boolean>>({});
  const [nc, setNc] = useState({ host: "", kind: "header", name: "", value: "" });
  const [chosenKeys, setChosenKeys] = useState<Record<string, boolean>>({});
  const [otherPath, setOtherPath] = useState("");
  const [extraPaths, setExtraPaths] = useState<string[]>([]);
  const [populating, setPopulating] = useState(false);
  const [applying, setApplying] = useState(false);
  const [updateRepo, setUpdateRepo] = useState("");
  // Log retention: "" means "use default"; the placeholder shows the default.
  const [logDays, setLogDays] = useState("");
  const [logRows, setLogRows] = useState("");
  const [apiWrites, setApiWrites] = useState(false);
  const [dindDefault, setDindDefault] = useState(true);
  // PR merge defaults. mergeStrategy "" = ask per repo on first merge.
  const [mergeStrategy, setMergeStrategy] = useState("");
  const [mergeMode, setMergeMode] = useState("sandbox");
  const [mergeAutoTeardown, setMergeAutoTeardown] = useState(true);
  // Global assistant capability (separate DB-backed setting; null until first run).
  const [assistantCap, setAssistantCap] = useState<"readonly" | "act" | null>(null);

  const linesToList = (s: string) => s.split("\n").map((x) => x.trim()).filter(Boolean);

  const load = useCallback(
    (okMsg?: string) => {
      getJSON<GlobalView>("/global/config")
        .then((data) => {
          setG(data);
          setMonitor((data.monitor_hosts || []).join("\n"));
          setPorts((data.mitm_ports || []).join("\n"));
          setNewCreds([]);
          setRemovedCreds({});
          const chosen: Record<string, boolean> = {};
          (data.ssh_keys || []).forEach((k) => (chosen[sshBasename(k)] = true));
          setChosenKeys(chosen);
          // custom-path global keys (outside ~/.ssh)
          const availNames = new Set((data.available_ssh_keys || []).map((k) => k.name));
          setExtraPaths((data.ssh_keys || []).filter((p) => !availNames.has(sshBasename(p))));
          setUpdateRepo(data.update_repo || "");
          setLogDays(data.log_retention_days ? String(data.log_retention_days) : "");
          setLogRows(data.log_max_rows ? String(data.log_max_rows) : "");
          setApiWrites(!!data.api_writes_enabled);
          setDindDefault(data.dind_default !== false); // default ON
          setMergeStrategy(data.merge_strategy || ""); // "" = ask per repo
          setMergeMode(data.merge_mode || "sandbox");
          setMergeAutoTeardown(data.merge_auto_teardown !== false); // default ON
          if (okMsg) setMsg({ text: okMsg, err: false });
        })
        .catch((e) => setMsg({ text: `failed to load: ${(e as Error).message}`, err: true }));
      // Assistant capability lives in its own DB-backed setting.
      getJSON<{ capability: "readonly" | "act" | null; configured: boolean }>("/api/chat/capability")
        .then((c) => setAssistantCap(c.configured ? c.capability : null))
        .catch(() => {});
    },
    [],
  );
  useEffect(() => load(), [load]);

  function collectSSHKeys(): string[] {
    const out: string[] = [];
    (g?.available_ssh_keys || []).forEach((k) => {
      if (chosenKeys[k.name]) out.push(k.name);
    });
    extraPaths.forEach((p) => {
      if (chosenKeys[sshBasename(p)] !== false) out.push(p);
    });
    return out;
  }

  // Assistant capability saves immediately (its own DB-backed setting, not part
  // of the Apply bundle).
  async function saveAssistantCap(cap: "readonly" | "act") {
    setAssistantCap(cap);
    try {
      const r = await fetch("/api/chat/capability", {
        method: "PUT",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ capability: cap }),
      });
      if (!r.ok) throw new Error(`HTTP ${r.status}`);
      setMsg({ text: `✓ assistant set to ${cap === "act" ? "can act" : "read-only"}`, err: false });
    } catch (e) {
      setMsg({ text: `couldn't update assistant: ${(e as Error).message}`, err: true });
    }
  }

  async function apply() {
    const edit: GlobalEdit = {};
    if (newCreds.length) edit.set_creds = newCreds.slice();
    const unset = Object.keys(removedCreds).filter((h) => removedCreds[h]);
    if (unset.length) edit.unset_creds = unset;
    edit.monitor_hosts = linesToList(monitor);
    edit.mitm_ports = linesToList(ports);
    edit.ssh_keys = collectSSHKeys();
    edit.update_repo = updateRepo.trim();
    // 0 clears the override → back to the built-in default.
    edit.log_retention_days = logDays.trim() ? Math.max(0, parseInt(logDays, 10) || 0) : 0;
    edit.log_max_rows = logRows.trim() ? Math.max(0, parseInt(logRows, 10) || 0) : 0;
    edit.api_writes_enabled = apiWrites;
    edit.dind_default = dindDefault;
    edit.merge_strategy = mergeStrategy; // "" clears the global default
    edit.merge_mode = mergeMode;
    edit.merge_auto_teardown = mergeAutoTeardown;
    setApplying(true);
    try {
      const r = await postJSON<{ results?: string[] }>("/global/apply", edit);
      load((r.results || []).join("  •  ") || "✓ saved");
    } catch (e) {
      setMsg({ text: `apply failed: ${(e as Error).message}`, err: true });
    } finally {
      setApplying(false);
    }
  }

  async function populate() {
    setPopulating(false);
    try {
      await postRaw("/global/populate", {});
      setPopulating(true);
      setMsg({ text: "complete the prompts in the terminal; credentials refresh here when done", err: false });
    } catch (e) {
      setMsg({ text: `could not start: ${(e as Error).message}`, err: true });
    }
  }

  function addCred() {
    const host = nc.host.trim().toLowerCase();
    const name = nc.name.trim();
    if (!host || !name || !nc.value) {
      setMsg({ text: "credential needs host, name, and value", err: true });
      return;
    }
    setNewCreds((cs) => [...cs, { host, kind: nc.kind, name, value: nc.value }]);
    setNc({ host: "", kind: "header", name: "", value: "" });
    setMsg({ text: "credential queued — click Apply", err: false });
  }

  if (!g) return <p className="muted" style={{ padding: "1rem" }}>loading global settings…</p>;

  const availRows = (g.available_ssh_keys || []).map((k) => ({ value: k.name, meta: [k.type, k.comment].filter(Boolean).join("  ") }));
  const extraRows = extraPaths.map((p) => ({ value: p, meta: "custom path" }));

  return (
    <>
      <header className="console-header">
        <div className="brand">
          <Link to="/" className="brand-back" title="Back to all projects">
            ←
          </Link>
          <span className="brand-name">corral</span>
          <span className="brand-sub">global settings</span>
        </div>
      </header>

      <main className="global-wrap">
        <p className="global-intro">
          Settings that apply across every project. Credentials here are shared by all projects (a project can still override per host).
          Defaults are inherited by new projects when you run <code>corral init</code>.
        </p>
        <div id="global-root">
          <section className="cfg-zone">
            <h3>
              Do Not Disturb <span className="muted">— quiet hours for notifications (this browser)</span>
            </h3>
            <label className="row" style={{ marginBottom: "0.5rem" }}>
              <input
                type="checkbox"
                checked={dnd.config.enabled}
                onChange={(e) => dnd.setEnabled(e.target.checked)}
              />
              <span>
                Enable Do Not Disturb — silence the chime and toasts outside your active hours.
                {dnd.config.enabled && dnd.quiet && <span className="dnd-now"> · quiet right now 🌙</span>}
              </span>
            </label>
            <div className="row dnd-hours" style={{ opacity: dnd.config.enabled ? 1 : 0.5 }}>
              <span className="cfg-label">Notify me between</span>
              <select
                className="cfg-edit dnd-hour-select"
                disabled={!dnd.config.enabled}
                value={dnd.config.startHour}
                onChange={(e) => dnd.setWindow(Number(e.target.value), dnd.config.endHour)}
              >
                {HOURS.map((h) => (
                  <option key={h} value={h}>
                    {fmtHour(h)}
                  </option>
                ))}
              </select>
              <span>and</span>
              <select
                className="cfg-edit dnd-hour-select"
                disabled={!dnd.config.enabled}
                value={dnd.config.endHour}
                onChange={(e) => dnd.setWindow(dnd.config.startHour, Number(e.target.value))}
              >
                {HOURS.map((h) => (
                  <option key={h} value={h}>
                    {fmtHour(h)}
                  </option>
                ))}
              </select>
              <span className="muted cfg-note">uses this browser's local time (default 9am–5pm)</span>
            </div>
          </section>

          <section className="cfg-zone">
            <h3>
              Shared credentials <span className="muted">— all projects, mtime-reloaded live</span>
            </h3>
            <div className="muted global-path">{g.creds_path}</div>
            <table className="cfg-creds">
              <thead>
                <tr>
                  <th>Host</th>
                  <th>Injects</th>
                  <th>Value</th>
                  <th></th>
                </tr>
              </thead>
              <tbody>
                {(g.credentials || []).length === 0 && (
                  <tr>
                    <td className="muted" colSpan={4}>
                      none set
                    </td>
                  </tr>
                )}
                {(g.credentials || []).map((c) => (
                  <tr key={c.host} style={{ opacity: removedCreds[c.host] ? 0.4 : 1 }}>
                    <td>{c.host}</td>
                    <td>
                      {c.kind}: {c.name}
                    </td>
                    <td className="cred-mask">{c.masked}</td>
                    <td>
                      <button className="cfg-cred-rm" onClick={() => { setRemovedCreds((r) => ({ ...r, [c.host]: true })); setMsg({ text: "credential queued for removal — click Apply", err: false }); }}>
                        remove
                      </button>
                    </td>
                  </tr>
                ))}
                {newCreds.map((c, i) => (
                  <tr key={`new-${i}`}>
                    <td>{c.host}</td>
                    <td>
                      {c.kind}: {c.name}
                    </td>
                    <td className="s-2xx">pending</td>
                    <td />
                  </tr>
                ))}
              </tbody>
            </table>
            <div className="cfg-cred-add">
              <input placeholder="host (api.foo.com)" value={nc.host} onChange={(e) => setNc({ ...nc, host: e.target.value })} />
              <select value={nc.kind} onChange={(e) => setNc({ ...nc, kind: e.target.value })}>
                <option value="header">header</option>
                <option value="url_param">url_param</option>
              </select>
              <input placeholder="name (X-API-Key)" value={nc.name} onChange={(e) => setNc({ ...nc, name: e.target.value })} />
              <input placeholder="value (secret)" type="password" value={nc.value} onChange={(e) => setNc({ ...nc, value: e.target.value })} />
              <button className="cfg-btn" onClick={addCred}>
                Add
              </button>
            </div>
            <div className="cfg-actions">
              <button className="cfg-btn cfg-btn-ghost" onClick={populate}>
                Populate from Claude…
              </button>
              <span className="muted">
                runs <code>claude setup-token</code> in a terminal below
              </span>
            </div>
            {populating && (
              <div className="populate-term" style={{ display: "block", height: 320 }}>
                <div className="screen-bar">
                  <i className="screen-dot" />
                  claude setup-token · answer the prompts
                </div>
                <XtermPane fullPath="/global/populate/ws" />
              </div>
            )}
          </section>

          <section className="cfg-zone">
            <h3>
              Defaults for new projects <span className="muted">— inherited at <code>corral init</code></span>
            </h3>
            <div className="cfg-field">
              <div className="cfg-label">Default monitor-list</div>
              <div className="cfg-value">
                <textarea className="cfg-edit" rows={3} spellCheck={false} value={monitor} onChange={(e) => setMonitor(e.target.value)} />
                <div className="muted cfg-note">empty = new projects monitor all allowed hosts</div>
              </div>
            </div>
            <div className="cfg-field">
              <div className="cfg-label">Default mitm-ports</div>
              <div className="cfg-value">
                <textarea className="cfg-edit" rows={2} spellCheck={false} value={ports} onChange={(e) => setPorts(e.target.value)} />
              </div>
            </div>
          </section>

          <section className="cfg-zone">
            <h3>
              Default SSH keys <span className="muted">— loaded by EVERY project's scoped agent</span>
            </h3>
            <div className="muted global-path">{g.ssh_keys_path}</div>
            <div className="cfg-field">
              <div className="cfg-label">Keys</div>
              <div className="cfg-value">
                {availRows.length + extraRows.length === 0 ? (
                  <div className="muted cfg-note">no keys found under ~/.ssh</div>
                ) : (
                  <div className="cfg-ssh-list">
                    {[...availRows, ...extraRows].map((r) => (
                      <label key={r.value} className="cfg-ssh-item">
                        <input
                          type="checkbox"
                          className="g-ssh-key"
                          checked={!!chosenKeys[sshBasename(r.value)]}
                          onChange={(e) => setChosenKeys((c) => ({ ...c, [sshBasename(r.value)]: e.target.checked }))}
                        />{" "}
                        <span className="cfg-ssh-name">{sshBasename(r.value)}</span>{" "}
                        {r.meta && <span className="muted cfg-ssh-meta">{r.meta}</span>}
                      </label>
                    ))}
                  </div>
                )}
                <div className="cfg-ssh-extra-add">
                  <input placeholder="other key path (~/.ssh/... or /abs/path)" value={otherPath} onChange={(e) => setOtherPath(e.target.value)} />
                  <button
                    className="cfg-btn cfg-btn-ghost"
                    onClick={() => {
                      const v = otherPath.trim();
                      if (!v) return;
                      setExtraPaths((ps) => (ps.includes(v) ? ps : [...ps, v]));
                      setChosenKeys((c) => ({ ...c, [sshBasename(v)]: true }));
                      setOtherPath("");
                    }}
                  >
                    + add path
                  </button>
                </div>
              </div>
            </div>
            <div className="muted cfg-note">
              The container can USE these keys (sign/push) but never reads the key bytes — only the agent socket is mounted. Projects can add more in their Config tab.
            </div>
          </section>

          <section className="cfg-zone">
            <h3>
              Update source <span className="muted">— where <code>corral update</code> pulls releases from</span>
            </h3>
            <div className="cfg-field">
              <div className="cfg-label">Repo or URL</div>
              <div className="cfg-value">
                <input
                  className="cfg-edit"
                  spellCheck={false}
                  placeholder={g.update_repo_default}
                  value={updateRepo}
                  onChange={(e) => setUpdateRepo(e.target.value)}
                />
                <div className="muted cfg-note">
                  A GitHub <code>owner/name</code>, or a full release URL for another host (e.g.{" "}
                  <code>https://git.example.com/owner/repo</code>) that uses the same{" "}
                  <code>/releases/latest</code> + <code>/releases/download/&lt;tag&gt;/…</code> layout. Leave blank
                  for the default (<code>{g.update_repo_default}</code>).
                </div>
              </div>
            </div>
          </section>

          <section className="cfg-zone">
            <h3>
              Log retention <span className="muted">— how long the <a href="/automations/logs">activity log</a> is kept</span>
            </h3>
            <div className="cfg-field">
              <div className="cfg-label">Keep for (days)</div>
              <div className="cfg-value">
                <input
                  className="cfg-edit dnd-hour-select"
                  type="number"
                  min={0}
                  placeholder={String(g.log_retention_days_default)}
                  value={logDays}
                  onChange={(e) => setLogDays(e.target.value)}
                />
                <div className="muted cfg-note">Blank = default ({g.log_retention_days_default} days). Older entries are pruned hourly.</div>
              </div>
            </div>
            <div className="cfg-field">
              <div className="cfg-label">Max entries</div>
              <div className="cfg-value">
                <input
                  className="cfg-edit dnd-hour-select"
                  type="number"
                  min={0}
                  placeholder={String(g.log_max_rows_default)}
                  value={logRows}
                  onChange={(e) => setLogRows(e.target.value)}
                />
                <div className="muted cfg-note">Blank = default ({g.log_max_rows_default.toLocaleString()} rows). The newest are kept.</div>
              </div>
            </div>
          </section>

          <section className="cfg-section">
            <h3>
              API access <span className="muted">— let the <code>corral api</code> CLI and Claude make changes</span>
            </h3>
            <div className="cfg-field">
              <label className="cfg-ssh-item">
                <input type="checkbox" checked={apiWrites} onChange={(e) => setApiWrites(e.target.checked)} />{" "}
                <span className="cfg-ssh-name">Allow API writes</span>
              </label>
              <div className="muted cfg-note">
                Reads (listing flows, logs, PRs) are always allowed. With this off, the CLI and Claude can look
                but not act; turn it on to let them start projects, create issues, and run flows. Off by default.
              </div>
            </div>
          </section>

          <section className="cfg-section">
            <h3>
              New sandboxes <span className="muted">— defaults for projects you create</span>
            </h3>
            <div className="cfg-field">
              <label className="cfg-ssh-item">
                <input type="checkbox" checked={dindDefault} onChange={(e) => setDindDefault(e.target.checked)} />{" "}
                <span className="cfg-ssh-name">Default Docker-in-Docker</span>
              </label>
              <div className="muted cfg-note">
                On (default): new sandboxes run <b>privileged</b> so Docker works inside them (build images, run
                <code> docker compose</code>). Off gives a tighter, unprivileged box — pick this if you don't need
                Docker and want stronger isolation. A single project can override this when it's created or in its
                Config tab.
              </div>
            </div>
          </section>

          <section className="cfg-section">
            <h3>
              PR merging <span className="muted">— defaults for the Merge button on a PR</span>
            </h3>
            <div className="cfg-field">
              <span className="cfg-ssh-name">Default merge mode</span>
              <select className="cfg-select" value={mergeMode} onChange={(e) => setMergeMode(e.target.value)}>
                <option value="sandbox">Merge with sandbox — rebase &amp; merge in a one-shot container</option>
                <option value="host">Merge with host — rebase &amp; merge on the host (fast, not sandboxed)</option>
                <option value="plain">Merge — plain gh merge, no rebase</option>
              </select>
              <div className="muted cfg-note">
                Which action the Merge button runs by default. The ▾ next to it always lets you pick a different
                mode per-merge. "Sandbox" and "host" run Claude with the editable <code>pr.merge</code> prompt
                (Automations → Prompts) to rebase onto the base branch, resolve conflicts, and merge; "host" skips
                the container for speed but <b>is not sandboxed</b>.
              </div>
            </div>
            <div className="cfg-field">
              <span className="cfg-ssh-name">Default merge strategy</span>
              <select className="cfg-select" value={mergeStrategy} onChange={(e) => setMergeStrategy(e.target.value)}>
                <option value="">Ask per repo (remember the first choice)</option>
                <option value="squash">Squash and merge</option>
                <option value="merge">Create a merge commit</option>
                <option value="rebase">Rebase and merge</option>
              </select>
              <div className="muted cfg-note">
                The method used when merging. A repo can set its own preference (which wins); leave this on "Ask per
                repo" to be prompted the first time you merge each repo and have that choice remembered for it. Only
                methods your GitHub repo actually allows are offered.
              </div>
            </div>
            <div className="cfg-field">
              <label className="cfg-ssh-item">
                <input type="checkbox" checked={mergeAutoTeardown} onChange={(e) => setMergeAutoTeardown(e.target.checked)} />{" "}
                <span className="cfg-ssh-name">Auto-remove the merge sandbox once merged</span>
              </label>
              <div className="muted cfg-note">
                On (default): after a "merge with sandbox" job lands the PR, its one-shot container is torn down
                automatically. Turn off if you want to inspect how conflicts were resolved before removing it.
              </div>
            </div>
          </section>

          <section className="cfg-section">
            <h3>
              Assistant <span className="muted">— what the app-wide Claude chat can do</span>
            </h3>
            <div className="cfg-field">
              <label className="cfg-ssh-item">
                <input
                  type="radio"
                  name="assistant-cap"
                  checked={assistantCap !== "act"}
                  onChange={() => saveAssistantCap("readonly")}
                />{" "}
                <span className="cfg-ssh-name">Read-only — look, don't change</span>
              </label>
              <label className="cfg-ssh-item">
                <input
                  type="radio"
                  name="assistant-cap"
                  checked={assistantCap === "act"}
                  onChange={() => saveAssistantCap("act")}
                />{" "}
                <span className="cfg-ssh-name">Can act — run corral api (create issues, start projects, run flows)</span>
              </label>
              <div className="muted cfg-note">
                Governs the app-wide chat dock. "Can act" still needs API writes (above) enabled before it can
                actually change anything.
                {assistantCap === null && " Not set yet — you'll be asked the first time you open the chat."}
              </div>
            </div>
          </section>

          <div className="cfg-actions">
            <button className="cfg-btn" disabled={applying} onClick={apply}>
              {applying ? "Applying…" : "Apply"}
            </button>
            {msg && <span className={`cfg-msg ${msg.err ? "s-4xx" : "s-2xx"}`}>{msg.text}</span>}
          </div>
        </div>
      </main>
    </>
  );
}
