import { useCallback, useEffect, useRef, useState } from "react";
import { delJSON, getJSON, postJSON, postRaw } from "../api/client";
import type {
  ConfigDiffEntry,
  ConfigEdit,
  ConfigView,
  CredSet,
  DindCache,
  SSHAvailableKey,
  SSHKeysStatus,
} from "../api/types";
import { fmtBytes } from "../lib/format";
import { SSHLoadModal } from "../components/SSHLoadModal";

// Config tab: fetch /p/<id>/config, render a live-reload zone and a
// restart-required zone, and drive the review->apply and restart flows.
// Port of config.js. Credential values are only sent on an explicit set;
// the read side masks them.

function api(projectId: string, p: string) {
  return `/p/${projectId}${p}`;
}
function sshKeyBasename(p: string): string {
  return p.replace(/\/$/, "").split("/").pop() || p;
}

export function ConfigTab({ projectId, refreshKey }: { projectId: string; refreshKey?: number }) {
  const [cfg, setCfg] = useState<ConfigView | null>(null);
  const [msg, setMsg] = useState<{ text: string; err: boolean } | null>(null);
  const [diff, setDiff] = useState<ConfigDiffEntry[] | null>(null);
  const [restarting, setRestarting] = useState(false);
  const [sshOpen, setSshOpen] = useState(false);
  const sshOnDone = useRef<(loaded: boolean) => void>(() => {});

  // live-zone editable fields
  const [allowed, setAllowed] = useState("");
  const [preset, setPreset] = useState("minimal");
  const [monitor, setMonitor] = useState("");
  const [ports, setPorts] = useState("");
  const [newCreds, setNewCreds] = useState<CredSet[]>([]);
  const [removedCreds, setRemovedCreds] = useState<Record<string, boolean>>({});
  const [nc, setNc] = useState({ host: "", kind: "header", name: "", value: "" });

  // restart-zone editable fields
  const [proxy, setProxy] = useState(false);
  const [passthrough, setPassthrough] = useState(false);
  const [dind, setDind] = useState(false);
  const [dindPorts, setDindPorts] = useState("");
  const [tmux, setTmux] = useState(false);
  const [seccomp, setSeccomp] = useState("");

  // DinD data-cache: which cache this project starts from ("" = none) + mode.
  const [cacheName, setCacheName] = useState("");
  const [cacheMode, setCacheMode] = useState<"copy" | "shared">("copy");
  const [caches, setCaches] = useState<DindCache[]>([]);
  const [saveCacheName, setSaveCacheName] = useState("");

  // SSH picker
  const [available, setAvailable] = useState<SSHAvailableKey[]>([]);
  const [checkedExtras, setCheckedExtras] = useState<Record<string, boolean>>({});
  const [otherPath, setOtherPath] = useState("");
  const [sshStatus, setSshStatus] = useState<SSHKeysStatus | null>(null);

  const linesToList = (s: string) => s.split("\n").map((x) => x.trim()).filter(Boolean);

  const load = useCallback(
    (okMsg?: string) => {
      getJSON<ConfigView>(api(projectId, "/config"))
        .then((c) => {
          setCfg(c);
          setAllowed((c.allowed_hosts || []).join("\n"));
          setPreset(c.mitm_preset || "minimal");
          setMonitor((c.monitor_hosts || []).join("\n"));
          setPorts((c.mitm_ports || []).join("\n"));
          setProxy(c.proxy_enabled);
          setPassthrough(c.passthrough_firewall);
          setDind(c.dind_enabled);
          setDindPorts((c.dind_ports || []).join("\n"));
          setCacheName(c.dind_cache?.name || "");
          setCacheMode(c.dind_cache?.mode === "shared" ? "shared" : "copy");
          setTmux(c.launch_tmux);
          setSeccomp(c.seccomp_mode || "");
          setNewCreds([]);
          setRemovedCreds({});
          // seed extras from ssh_keys not covered by globals (by basename)
          const globalSet = new Set((c.ssh_keys_global || []).map(sshKeyBasename));
          const extras: Record<string, boolean> = {};
          (c.ssh_keys || []).forEach((k) => {
            extras[k] = true;
          });
          setCheckedExtras(extras);
          void globalSet;
          if (okMsg) setMsg({ text: okMsg, err: false });
        })
        .catch((e) => setMsg({ text: `load failed: ${(e as Error).message}`, err: true }));
    },
    [projectId],
  );

  useEffect(() => load(), [load]);

  // DinD caches are host-wide (not per-project), so this list is independent of
  // the project config. Fetched once; refreshed after a save/delete.
  const refreshCaches = useCallback(() => {
    getJSON<{ caches: DindCache[] }>("/api/dind/caches")
      .then((r) => setCaches(r.caches || []))
      .catch(() => setCaches([]));
  }, []);
  useEffect(() => refreshCaches(), [refreshCaches]);

  // dirty = the user has unsaved edits vs. the last-loaded config. Used to gate
  // the on-open refresh so re-entering the Config tab doesn't discard in-progress
  // edits. Compares the fields a reload would overwrite.
  const norm = (s: string) => s.split("\n").map((x) => x.trim()).filter(Boolean).join("\n");
  const dirty =
    !!cfg &&
    (norm(allowed) !== norm((cfg.allowed_hosts || []).join("\n")) ||
      preset !== (cfg.mitm_preset || "minimal") ||
      norm(monitor) !== norm((cfg.monitor_hosts || []).join("\n")) ||
      norm(ports) !== norm((cfg.mitm_ports || []).join("\n")) ||
      newCreds.length > 0 ||
      Object.keys(removedCreds).length > 0 ||
      proxy !== cfg.proxy_enabled ||
      passthrough !== cfg.passthrough_firewall ||
      dind !== cfg.dind_enabled ||
      norm(dindPorts) !== norm((cfg.dind_ports || []).join("\n")) ||
      cacheName !== (cfg.dind_cache?.name || "") ||
      (!!cacheName && cacheMode !== (cfg.dind_cache?.mode === "shared" ? "shared" : "copy")) ||
      tmux !== cfg.launch_tmux ||
      seccomp !== (cfg.seccomp_mode || ""));
  const dirtyRef = useRef(dirty);
  dirtyRef.current = dirty;

  // Re-fetch fresh /config when the tab is (re)opened (refreshKey bumps), unless
  // the user has unsaved edits — then keep their in-progress state. The first
  // mount is handled by the load() effect above; skip refreshKey === 0.
  useEffect(() => {
    if (!refreshKey) return;
    if (!dirtyRef.current) load();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [refreshKey]);

  // Also re-fetch when the window regains focus / becomes visible, so a change
  // made elsewhere (e.g. monitoring a host from the Mitm tab, or the CLI) shows
  // up without a manual reload. Skipped while dirty so it never clobbers edits.
  useEffect(() => {
    const onFocus = () => {
      if (document.visibilityState !== "hidden" && !dirtyRef.current) load();
    };
    window.addEventListener("focus", onFocus);
    document.addEventListener("visibilitychange", onFocus);
    return () => {
      window.removeEventListener("focus", onFocus);
      document.removeEventListener("visibilitychange", onFocus);
    };
  }, [load]);

  // SSH available keys + status
  useEffect(() => {
    getJSON<{ keys: SSHAvailableKey[] }>(api(projectId, "/sshkeys/available"))
      .then((d) => setAvailable(d.keys || []))
      .catch(() => {});
  }, [projectId]);

  const refreshSSHStatus = useCallback(() => {
    getJSON<SSHKeysStatus>(api(projectId, "/sshkeys/status"))
      .then(setSshStatus)
      .catch(() => setSshStatus(null));
  }, [projectId]);
  useEffect(() => refreshSSHStatus(), [refreshSSHStatus]);

  // Collect the currently-checked project EXTRA keys (globals are locked/managed elsewhere).
  const collectSSHExtras = useCallback((): string[] => {
    const globalSet = new Set((cfg?.ssh_keys_global || []).map(sshKeyBasename));
    return Object.keys(checkedExtras).filter((k) => checkedExtras[k] && !globalSet.has(sshKeyBasename(k)));
  }, [checkedExtras, cfg]);

  function collectLiveEdit(): ConfigEdit {
    const edit: ConfigEdit = {};
    edit.allowed_hosts = linesToList(allowed);
    edit.mitm_preset = preset;
    if (preset === "custom") edit.monitor_hosts = linesToList(monitor);
    edit.mitm_ports = linesToList(ports);
    if (newCreds.length) edit.set_creds = newCreds.slice();
    const unset = Object.keys(removedCreds).filter((h) => removedCreds[h]);
    if (unset.length) edit.unset_creds = unset;
    return edit;
  }
  function collectRestartEdit(): ConfigEdit {
    return {
      proxy_enabled: proxy,
      passthrough_firewall: passthrough,
      dind_enabled: dind,
      dind_ports: linesToList(dindPorts),
      // null clears the cache; an object sets it. Only meaningful with DinD on.
      dind_cache: cacheName ? { name: cacheName, mode: cacheMode } : null,
      launch_tmux: tmux,
      seccomp_mode: seccomp,
      ssh_keys: collectSSHExtras(),
    };
  }

  async function reviewAndApply() {
    const edit = collectLiveEdit();
    try {
      const res = await postJSON<{ entries?: ConfigDiffEntry[] }>(api(projectId, "/config/diff"), edit);
      const entries = res.entries || [];
      if (!entries.length) {
        setMsg({ text: "no changes", err: false });
        return;
      }
      setDiff(entries);
    } catch (e) {
      setMsg({ text: `diff failed: ${(e as Error).message}`, err: true });
    }
  }

  async function confirmApply() {
    const edit = collectLiveEdit();
    try {
      const r = await postJSON<{ results?: string[] }>(api(projectId, "/config/apply"), edit);
      setDiff(null);
      load((r.results || []).join("  •  ") || "✓ applied");
    } catch (e) {
      setDiff(null);
      setMsg({ text: `apply failed: ${(e as Error).message}`, err: true });
    }
  }

  const restartRequest = useCallback(
    async (edit: ConfigEdit) => {
      setRestarting(true);
      try {
        const res = await postRaw(api(projectId, "/config/restart"), edit);
        const body = await res.json().catch(() => ({}));
        if (res.status === 409 && body?.ssh_keys_pending) {
          setMsg({ text: "load SSH keys to continue the restart…", err: false });
          setRestarting(false);
          sshOnDone.current = () => {
            setMsg({ text: "keys loaded — restarting…", err: false });
            restartRequest(edit);
          };
          setSshOpen(true);
          return;
        }
        if (res.status >= 400) throw new Error(body?.error || `HTTP ${res.status}`);
        setMsg({ text: (body?.results || []).join("  •  "), err: false });
      } catch (e) {
        setMsg({ text: `restart failed: ${(e as Error).message}`, err: true });
      } finally {
        setRestarting(false);
      }
    },
    [projectId],
  );

  function doRestart() {
    if (!window.confirm("Restart this project now? This kills the container and any running session in it.")) return;
    restartRequest(collectRestartEdit());
  }

  // Snapshot this project's current inner-docker data root into a named cache.
  // Server-side this is a full container-mediated copy — slow for big data roots
  // — so we surface an in-progress message and disable the button.
  const [savingCache, setSavingCache] = useState(false);
  async function saveAsCache() {
    const nm = saveCacheName.trim();
    if (!/^[a-zA-Z0-9_-]{1,64}$/.test(nm)) {
      setMsg({ text: "cache name: letters, digits, dashes, underscores (max 64)", err: true });
      return;
    }
    setSavingCache(true);
    setMsg({ text: `saving cache “${nm}” — copying the DinD volume, this can take a while…`, err: false });
    try {
      await postJSON("/api/dind/caches", { name: nm, project: projectId });
      setSaveCacheName("");
      setMsg({ text: `✓ saved cache “${nm}”`, err: false });
      refreshCaches();
    } catch (e) {
      setMsg({ text: `save cache failed: ${(e as Error).message}`, err: true });
    } finally {
      setSavingCache(false);
    }
  }

  // saveAsRepoCache snapshots this project's inner-docker data into the
  // repo-scoped baseline (repo-<repoId>), the cache new projects from this repo
  // auto-attach. If it already exists, we delete-then-recreate so "save" always
  // refreshes the baseline to this project's current state.
  async function saveAsRepoCache(repoId: string, exists: boolean) {
    const nm = `repo-${repoId}`;
    if (exists && !window.confirm(`Update the repo baseline “${nm}” to this project's current inner-docker data? New projects from this repo will start from it.`)) return;
    setSavingCache(true);
    setMsg({ text: `saving repo baseline “${nm}” — copying the DinD volume, this can take a while…`, err: false });
    try {
      if (exists) await delJSON(`/api/dind/caches/${encodeURIComponent(nm)}`);
      await postJSON("/api/dind/caches", { name: nm, project: projectId });
      setMsg({ text: `✓ saved repo baseline “${nm}” — new projects from this repo will reuse it`, err: false });
      refreshCaches();
    } catch (e) {
      setMsg({ text: `save repo cache failed: ${(e as Error).message}`, err: true });
    } finally {
      setSavingCache(false);
    }
  }

  async function deleteCache(nm: string) {
    if (!window.confirm(`Delete DinD cache “${nm}”? Projects still set to use it will fail to start until you re-point them.`)) return;
    try {
      await delJSON(`/api/dind/caches/${encodeURIComponent(nm)}`);
      if (cacheName === nm) setCacheName("");
      setMsg({ text: `✓ deleted cache “${nm}”`, err: false });
      refreshCaches();
    } catch (e) {
      setMsg({ text: `delete cache failed: ${(e as Error).message}`, err: true });
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
    setMsg({ text: "credential queued — click Review changes to apply", err: false });
  }

  // The SSH picker rows: globals (locked, pre-checked) + available extras + custom paths.
  const globalBasenames = new Set((cfg?.ssh_keys_global || []).map(sshKeyBasename));
  const availableRows = available.map((k) => {
    const inGlobal = globalBasenames.has(sshKeyBasename(k.name));
    return { value: k.name, locked: inGlobal, checked: inGlobal || !!checkedExtras[k.name], meta: k.comment || k.type };
  });
  const seen = new Set(availableRows.map((r) => sshKeyBasename(r.value)));
  const customRows = (cfg?.ssh_keys || [])
    .filter((p) => !seen.has(sshKeyBasename(p)))
    .map((p) => ({ value: p, locked: false, checked: true, meta: "custom path" }));
  const sshRows = [...availableRows, ...customRows];

  const effectiveNames = new Set<string>();
  (cfg?.ssh_keys_global || []).forEach((k) => effectiveNames.add(sshKeyBasename(k)));
  collectSSHExtras().forEach((k) => effectiveNames.add(sshKeyBasename(k)));

  if (!cfg) return <p className="muted">loading config…</p>;

  return (
    <div id="config-root">
      <p className="cfg-status">
        {cfg.container_up ? (
          <span className="s-2xx">container running — live changes apply immediately</span>
        ) : (
          <span className="muted">container not running — live changes save for next start</span>
        )}
      </p>

      <section className="cfg-zone">
        <h3>
          Live <span className="muted">— hot-reloaded, no restart</span>
        </h3>

        <Field label="Allowed hosts">
          <textarea className="cfg-edit" rows={Math.max(2, allowed.split("\n").length + 1)} spellCheck={false} value={allowed} onChange={(e) => setAllowed(e.target.value)} />
        </Field>

        <Field label="Capture (mitm)">
          <select
            className="cfg-select"
            value={preset}
            onChange={(e) => setPreset(e.target.value)}
          >
            <option value="minimal">Minimal — Claude + GitHub only</option>
            <option value="all">All — every allowed host</option>
            <option value="none">None — decrypt nothing</option>
            <option value="custom">Custom — the host list below</option>
          </select>
          {preset === "custom" && (
            <div>
              <MonitorList
                value={monitor}
                onChange={setMonitor}
                onPersist={async (hosts) => {
                  // Persist + reload immediately (like the Mitm tab's Monitor
                  // button), so add/remove take effect without Review→apply and
                  // there's no staged state to get flipped back.
                  setMonitor(hosts.join("\n"));
                  try {
                    const r = await postJSON<{ results?: string[] }>(api(projectId, "/config/apply"), {
                      mitm_preset: "custom",
                      monitor_hosts: hosts,
                    });
                    setMsg({ text: (r.results || []).join("  •  ") || "✓ monitor list updated", err: false });
                  } catch (e) {
                    setMsg({ text: `apply failed: ${(e as Error).message}`, err: true });
                  }
                }}
              />
              <div className="muted cfg-note">
                only these hosts are decrypted; others allowed+logged but direct-dialed. Hosts with an injected credential are always
                decrypted regardless (required for credential injection) and aren't listed here.
              </div>
            </div>
          )}
          <div className="cfg-warn">
            <strong>⚠ Monitoring a host makes the proxy present its own CA.</strong> Clients that pin certificates or statically link their
            TLS with a bundled root store won’t trust it — the handshake fails and the request never completes (so a broken host won’t even
            appear in the Mitm tab). If a host breaks when monitored, remove it here; it falls back to direct-dial (undecrypted) and works again.
          </div>
        </Field>

        <Field label="Mitm ports">
          <textarea className="cfg-edit" rows={Math.max(2, ports.split("\n").length + 1)} spellCheck={false} value={ports} onChange={(e) => setPorts(e.target.value)} />
          <div className="muted cfg-note">other ports (ssh, socks, git-over-ssh) are direct-dialed</div>
        </Field>

        <Field label="Credentials">
          <table className="cfg-creds">
            <tbody>
              {(cfg.credentials || []).map((c) => (
                <tr key={c.host} style={{ opacity: removedCreds[c.host] ? 0.4 : 1 }}>
                  <td>{c.host}</td>
                  <td>
                    {c.kind}: {c.name}
                  </td>
                  <td className="muted">{c.masked}</td>
                  <td>
                    <button className="cfg-cred-rm" onClick={() => { setRemovedCreds((r) => ({ ...r, [c.host]: true })); setMsg({ text: `credential ${c.host} queued for removal — Review to apply`, err: false }); }}>
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
              Add credential
            </button>
          </div>
        </Field>

        <div className="cfg-actions">
          <button className="cfg-btn" onClick={reviewAndApply}>
            Review changes
          </button>
          {msg && <span className={`cfg-msg ${msg.err ? "s-4xx" : "s-2xx"}`}>{msg.text}</span>}
        </div>
      </section>

      <section className="cfg-zone">
        <h3>
          Restart required <span className="muted">— needs a project restart</span>
        </h3>

        <Field label="Workspace">
          <code>{cfg.workspace}</code>
        </Field>
        <Field label="Network protection">
          <Toggle checked={proxy} onChange={setProxy} />
          <div className="muted cfg-note">On: traffic goes through the proxy — MITM inspection, credential injection, and the allowlist firewall. Off: fully open network (no proxy, no firewall).</div>
        </Field>
        <Field label="Passthrough firewall">
          <Toggle checked={passthrough} onChange={setPassthrough} />
          <div className="muted cfg-note">Keeps the proxy on (inspection + credentials) but ALLOWS + logs unknown domains instead of blocking, and permits direct TCP so <code>git</code> over SSH works. Needs Network protection on.</div>
        </Field>
        <Field label="Docker-in-Docker">
          <Toggle checked={dind} onChange={setDind} />
        </Field>
        <Field label="Published ports">
          <textarea className="cfg-edit" rows={Math.max(2, dindPorts.split("\n").length + 1)} spellCheck={false} value={dindPorts} onChange={(e) => setDindPorts(e.target.value)} />
        </Field>
        {dind && (
          <Field label="Start from cache">
            <div className="cfg-cache">
              <div className="cfg-cache-row">
                <select className="cfg-select" value={cacheName} onChange={(e) => setCacheName(e.target.value)}>
                  <option value="">None — fresh, empty inner docker</option>
                  {caches.map((c) => (
                    <option key={c.name} value={c.name}>
                      {c.name} ({fmtBytes(c.bytes)})
                    </option>
                  ))}
                </select>
                {cacheName && (
                  <select className="cfg-select" value={cacheMode} onChange={(e) => setCacheMode(e.target.value === "shared" ? "shared" : "copy")}>
                    <option value="copy">Copy — throwaway (cache untouched)</option>
                    <option value="shared">Shared — changes persist to the cache</option>
                  </select>
                )}
              </div>
              <div className="muted cfg-note">
                A cache is a prebuilt inner-docker data root (images + volumes) so a project doesn't rebuild/re-seed each time. <b>Copy</b> seeds a fresh per-project volume from the cache on first start; <b>Shared</b> mounts the cache directly so a migration writes back. Changing this needs a restart.
              </div>

              {cfg?.repo_id &&
                (() => {
                  const repoId = cfg.repo_id!;
                  const repoCacheName = `repo-${repoId}`;
                  const exists = caches.some((c) => c.name === repoCacheName);
                  return (
                    <div className="cfg-repo-cache">
                      <div className="cfg-repo-cache-head">
                        <b>Repo baseline</b>
                        <code>{repoCacheName}</code>
                        {exists ? (
                          <span className="cfg-repo-cache-badge on">saved · reused by new projects from this repo</span>
                        ) : (
                          <span className="cfg-repo-cache-badge">not saved yet</span>
                        )}
                      </div>
                      <div className="muted cfg-note">
                        Save this project's built images + seeded volumes as the shared baseline for this repo. New projects created from the same repo auto-start from it (copy mode) instead of building from an empty inner docker.
                      </div>
                      <button className="cfg-btn" disabled={savingCache} onClick={() => saveAsRepoCache(repoId, exists)}>
                        {savingCache ? "Saving…" : exists ? "Update repo baseline" : "Save as repo baseline"}
                      </button>
                    </div>
                  );
                })()}

              <div className="cfg-cache-save">
                <input
                  className="cfg-edit cfg-cache-name"
                  placeholder="new-cache-name"
                  value={saveCacheName}
                  spellCheck={false}
                  onChange={(e) => setSaveCacheName(e.target.value)}
                />
                <button className="cfg-btn" disabled={savingCache || !saveCacheName.trim()} onClick={saveAsCache}>
                  {savingCache ? "Saving…" : "Save current data as cache"}
                </button>
              </div>
              <div className="muted cfg-note">Snapshots this project's current inner-docker data (built images + seeded volumes) into a reusable cache. Full copy — can take a while for large data roots.</div>

              {caches.length > 0 && (
                <ul className="cfg-cache-list">
                  {caches.map((c) => (
                    <li key={c.name}>
                      <code>{c.name}</code> <span className="muted">{fmtBytes(c.bytes)}</span>
                      <button className="cfg-cache-del" title="Delete cache" onClick={() => deleteCache(c.name)}>
                        ×
                      </button>
                    </li>
                  ))}
                </ul>
              )}
            </div>
          </Field>
        )}
        <Field label="Launch tmux">
          <Toggle checked={tmux} onChange={setTmux} />
        </Field>
        <Field label="Seccomp">
          <select className="cfg-select" value={seccomp} onChange={(e) => setSeccomp(e.target.value)}>
            <option value="">Default (Docker profile)</option>
            <option value="unconfined">Unconfined — no filtering</option>
          </select>
          <div className="muted cfg-note">unconfined allows syscalls the default profile blocks (e.g. Erlang/BEAM). No effect with Docker-in-Docker (already privileged).</div>
        </Field>

        <Field label="SSH keys">
          <div className="cfg-ssh-list">
            {sshRows.length === 0 && <div className="muted cfg-note">no keys found under ~/.ssh</div>}
            {sshRows.map((r) => (
              <label key={r.value} className={`cfg-ssh-item${r.locked ? " locked" : ""}`}>
                <input
                  type="checkbox"
                  className="cfg-ssh-key"
                  checked={r.locked ? true : !!checkedExtras[r.value]}
                  disabled={r.locked}
                  onChange={(e) => setCheckedExtras((c) => ({ ...c, [r.value]: e.target.checked }))}
                />
                <span className="cfg-ssh-name">{sshKeyBasename(r.value)}</span>{" "}
                {r.meta && <span className="muted cfg-ssh-meta">{r.meta}</span>}
                {r.locked && <span className="cfg-ssh-badge">from global</span>}
              </label>
            ))}
          </div>
          <div className="cfg-ssh-extra-add">
            <input placeholder="other key path (~/.ssh/... or /abs/path)" value={otherPath} onChange={(e) => setOtherPath(e.target.value)} />
            <button
              className="cfg-btn cfg-btn-ghost"
              onClick={() => {
                const v = otherPath.trim();
                if (!v) return;
                setCheckedExtras((c) => ({ ...c, [v]: true }));
                setOtherPath("");
              }}
            >
              + add path
            </button>
          </div>
          <div className="muted cfg-note">will load: {[...effectiveNames].join(", ") || "no keys — no ssh-agent is mounted"}</div>
          <div className="cfg-ssh-load">
            <SSHStatusLine status={sshStatus} onRestart={() => restartRequest(collectRestartEdit())} />{" "}
            <button
              className="cfg-btn"
              onClick={async () => {
                // Persist the current selection first (so the load PTY targets it),
                // then open the modal — mirrors config.js.
                await postRaw(api(projectId, "/sshkeys/select"), { ssh_keys: collectSSHExtras() }).catch(() => {});
                sshOnDone.current = (loaded) => {
                  refreshSSHStatus();
                  if (loaded && cfg && !cfg.container_up) {
                    setMsg({ text: "keys loaded — starting project…", err: false });
                    postRaw(api(projectId, "/start")).catch(() => {});
                  }
                };
                setSshOpen(true);
              }}
            >
              Load keys…
            </button>
          </div>
        </Field>

        <div className="cfg-actions">
          <button className="cfg-btn cfg-btn-danger" disabled={restarting} onClick={doRestart}>
            {restarting ? "restarting…" : "Restart project now"}
          </button>
          <span className="muted"> — interrupts the running session in this project</span>
        </div>
      </section>

      {diff && (
        <div className="cfg-modal" style={{ display: "block" }}>
          <div className="cfg-modal-box">
            <h3>Review changes</h3>
            <table className="cfg-diff">
              <tbody>
                {diff.map((e, i) => (
                  <tr key={i}>
                    <td className="cfg-diff-field">{e.field}</td>
                    <td>{e.change}</td>
                  </tr>
                ))}
              </tbody>
            </table>
            <div className="cfg-modal-actions">
              <button className="cfg-btn" onClick={confirmApply}>
                Confirm &amp; apply
              </button>
              <button className="cfg-btn cfg-btn-ghost" onClick={() => setDiff(null)}>
                Cancel
              </button>
            </div>
          </div>
        </div>
      )}

      {sshOpen && (
        <SSHLoadModal
          projectId={projectId}
          onDone={(loaded) => {
            setSshOpen(false);
            const cb = sshOnDone.current;
            sshOnDone.current = () => {};
            cb(loaded);
          }}
        />
      )}
    </div>
  );
}

// MonitorList renders the custom monitor-hosts as a managed list of removable
// chips plus an add input. Add/remove persist IMMEDIATELY via onPersist (POST
// config/apply + proxy reload) — like the Mitm tab's Monitor button — so there's
// no staged state to get flipped back, and ✕ takes effect at once. onChange
// keeps the parent's local mirror in sync for the dirty check. Add supports
// comma/space/newline-separated paste.
function MonitorList({
  value,
  onChange,
  onPersist,
}: {
  value: string;
  onChange: (v: string) => void;
  onPersist: (hosts: string[]) => void;
}) {
  const [entry, setEntry] = useState("");
  const hosts = value
    .split("\n")
    .map((s) => s.trim())
    .filter(Boolean);

  const write = (list: string[]) => {
    // de-dupe (case-insensitive), preserve order
    const seen = new Set<string>();
    const out: string[] = [];
    for (const h of list) {
      const k = h.toLowerCase();
      if (h && !seen.has(k)) {
        seen.add(k);
        out.push(h);
      }
    }
    onChange(out.join("\n"));
    onPersist(out);
  };

  const add = () => {
    const parts = entry
      .split(/[\s,]+/)
      .map((s) => s.trim())
      .filter(Boolean);
    if (!parts.length) return;
    write([...hosts, ...parts]);
    setEntry("");
  };
  const remove = (h: string) => write(hosts.filter((x) => x !== h));

  return (
    <div className="monitor-list">
      {hosts.length === 0 ? (
        <div className="muted cfg-note" style={{ marginBottom: 6 }}>
          nothing monitored yet — every allowed host is direct-dialed (undecrypted). Add a host to decrypt it.
        </div>
      ) : (
        <div className="monitor-chips">
          {hosts.map((h) => (
            <span className="monitor-chip" key={h}>
              <span className="monitor-chip-host">{h}</span>
              <button type="button" className="monitor-chip-x" title={`Stop monitoring ${h}`} onClick={() => remove(h)}>
                ✕
              </button>
            </span>
          ))}
        </div>
      )}
      <div className="monitor-add">
        <input
          type="text"
          placeholder="add a host (api.example.com)"
          autoComplete="off"
          spellCheck={false}
          value={entry}
          onChange={(e) => setEntry(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter") {
              e.preventDefault();
              add();
            }
          }}
        />
        <button type="button" className="cfg-btn cfg-btn-ghost" onClick={add}>
          + add
        </button>
      </div>
    </div>
  );
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="cfg-field">
      <div className="cfg-label">{label}</div>
      <div className="cfg-value">{children}</div>
    </div>
  );
}

function Toggle({ checked, onChange }: { checked: boolean; onChange: (v: boolean) => void }) {
  return (
    <label className="cfg-inline">
      <input type="checkbox" checked={checked} onChange={(e) => onChange(e.target.checked)} /> <span className="cfg-slider" />
    </label>
  );
}

function SSHStatusLine({ status, onRestart }: { status: SSHKeysStatus | null; onRestart: () => void }) {
  if (!status) return <span className="muted">checking…</span>;
  if (!status.configured) return <span className="muted">no keys configured</span>;
  if (status.container_stale) {
    return (
      <span className="attention">
        ⚠ keys loaded on host, but the running container can't reach them — restart it to use SSH.{" "}
        <button className="cfg-btn cfg-btn-danger" onClick={onRestart}>
          Restart container
        </button>
      </span>
    );
  }
  if (status.loaded) return <span className="s-2xx">✓ {status.count} key(s) loaded — start won't prompt</span>;
  return <span className="muted">not loaded — start will need passphrases</span>;
}
