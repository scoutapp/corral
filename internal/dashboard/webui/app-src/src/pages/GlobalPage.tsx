import { useCallback, useEffect, useState } from "react";
import { Link } from "../router";
import { getJSON, postJSON, postRaw } from "../api/client";
import type { CredSet, GlobalEdit, GlobalView } from "../api/types";
import { XtermPane } from "../components/XtermPane";
import { useBodyClass } from "../hooks/useBodyClass";

// Global settings: shared credentials (masked), cross-project defaults
// (monitor-list, mitm-ports) new projects inherit, default SSH keys loaded by
// every project's scoped agent, and a Populate button that runs the interactive
// `claude setup-token` flow in a bridged terminal. Port of global.js.

function sshBasename(p: string): string {
  return p.replace(/^.*\//, "");
}

export function GlobalPage() {
  useBodyClass("console");
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
          if (okMsg) setMsg({ text: okMsg, err: false });
        })
        .catch((e) => setMsg({ text: `failed to load: ${(e as Error).message}`, err: true }));
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

  async function apply() {
    const edit: GlobalEdit = {};
    if (newCreds.length) edit.set_creds = newCreds.slice();
    const unset = Object.keys(removedCreds).filter((h) => removedCreds[h]);
    if (unset.length) edit.unset_creds = unset;
    edit.monitor_hosts = linesToList(monitor);
    edit.mitm_ports = linesToList(ports);
    edit.ssh_keys = collectSSHKeys();
    edit.update_repo = updateRepo.trim();
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
