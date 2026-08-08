import { Fragment, useCallback, useEffect, useRef, useState } from "react";
import { getJSON, getText, postJSON } from "../api/client";
import type { ConfigView, DirectHost, MitmFlow, MitmMessage } from "../api/types";
import { Modal } from "../components/Modal";

// localStorage flag: once the user acknowledges the CA-trust caveat with
// "don't show again", the Monitor confirm modal is skipped on future clicks.
const MONITOR_WARN_KEY = "sandclaude.monitorWarnAck";

// Mitm tab: mitmweb's decrypted flows MERGED with the proxy log's direct-dialed
// hosts, so every contacted host is visible. Decrypted rows expand to show
// headers/bodies; direct-dialed rows are dimmed with a "not decrypted" badge and
// a live "Monitor this host" action that adds the host to monitor_hosts and
// SIGHUPs the proxy — future requests to it are then decrypted (the already-
// completed one can't be retroactively decrypted). Port + extension of mitm.js.

function api(projectId: string, p: string) {
  return `/p/${projectId}${p}`;
}
function fmtBytes(n?: number): string {
  if (n == null || n < 0) return "";
  if (n < 1024) return `${n}b`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)}kb`;
  return `${(n / (1024 * 1024)).toFixed(1)}mb`;
}
function pad(n: number) {
  return (n < 10 ? "0" : "") + n;
}
function fmtWhenEpoch(t?: number): string {
  if (!t) return "";
  const d = new Date(t * 1000);
  return `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`;
}
// The proxy log stamps "YYYY/MM/DD HH:MM:SS" — show just HH:MM:SS.
function fmtWhenLog(s: string): string {
  const m = s.match(/(\d\d):(\d\d):(\d\d)/);
  return m ? `${m[1]}:${m[2]}:${m[3]}` : s;
}
function statusClass(code: number): string {
  if (!code) return "s-pending";
  if (code >= 500) return "s-5xx";
  if (code >= 400) return "s-4xx";
  if (code >= 300) return "s-3xx";
  return "s-2xx";
}
function fmtDuration(f: MitmFlow): string {
  const r = f.response;
  if (!r || !r.timestamp_end || !f.request?.timestamp_start) return "";
  return `${Math.round((r.timestamp_end - f.request.timestamp_start) * 1000)}ms`;
}
function contentTypeOf(msg?: MitmMessage): string {
  for (const h of msg?.headers || []) if (h[0].toLowerCase() === "content-type") return h[1];
  return "";
}

// Unified row model: a decrypted mitm flow, or one direct-dialed request.
type Row =
  | { kind: "flow"; ts: number; flow: MitmFlow }
  | { kind: "direct"; ts: number; host: string; when: string; key: string };

const BODY_CAP = 512 * 1024;

function BodySlot({ projectId, flowId, side, msg }: { projectId: string; flowId: string; side: "request" | "response"; msg?: MitmMessage }) {
  const [html, setHtml] = useState<string>("loading…");
  useEffect(() => {
    let alive = true;
    if (msg?.contentLength === 0) {
      setHtml("(empty)");
      return;
    }
    getText(api(projectId, `/mitm/flows/${flowId}/${side}/content`))
      .then((text) => {
        if (!alive) return;
        if (text.length > BODY_CAP) {
          setHtml(`body too large to display (${fmtBytes(text.length)})`);
          return;
        }
        let out = text;
        if (contentTypeOf(msg).toLowerCase().includes("json")) {
          try {
            out = JSON.stringify(JSON.parse(text), null, 2);
          } catch {
            /* leave raw */
          }
        }
        setHtml(out || "(empty)");
      })
      .catch((e) => alive && setHtml(`failed to load body: ${(e as Error).message}`));
    return () => {
      alive = false;
    };
  }, [projectId, flowId, side, msg]);
  return <pre className="mitm-body">{html}</pre>;
}

function HeaderTable({ headers }: { headers?: [string, string][] }) {
  if (!headers || !headers.length) return <div className="mitm-empty">(none)</div>;
  return (
    <table className="mitm-headers">
      <tbody>
        {headers.map((h, i) => (
          <tr key={i}>
            <td>{h[0]}</td>
            <td>{h[1]}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

export function MitmTab({ projectId, mitmUp }: { projectId: string; mitmUp: boolean }) {
  const [flows, setFlows] = useState<MitmFlow[]>([]);
  const [direct, setDirect] = useState<DirectHost[]>([]);
  const [filter, setFilter] = useState(""); // live input
  const [query, setQuery] = useState(""); // debounced -> server-side direct search
  const [expanded, setExpanded] = useState<Record<string, boolean>>({});
  const [status, setStatus] = useState("loading flows…");
  const [statusErr, setStatusErr] = useState(false);
  const [monitoring, setMonitoring] = useState<Record<string, "busy" | "done">>({});
  // Hosts currently in the monitor list (from /config) — a direct row for one of
  // these is HISTORICAL (it was direct-dialed then; new traffic is decrypted), so
  // it shows "now monitored" instead of a Monitor button. Lowercased hostnames.
  const [monitoredSet, setMonitoredSet] = useState<Set<string>>(new Set());
  const visibleRef = useRef(true);

  // CA-trust caveat gate. The first time you click "Monitor", a modal explains
  // the caveat and requires an explicit "I understand" acknowledgment. Once
  // acknowledged (persisted), future Monitor clicks act immediately. The ack
  // checkbox in the modal reflects the persisted state (stays selected).
  const [confirmHost, setConfirmHost] = useState<string | null>(null);
  const [understood, setUnderstood] = useState(false); // per-open acknowledgment gate
  const [ack, setAck] = useState<boolean>(() => {
    try {
      return localStorage.getItem(MONITOR_WARN_KEY) === "1";
    } catch {
      return false;
    }
  });
  const persistAck = (v: boolean) => {
    setAck(v);
    try {
      localStorage.setItem(MONITOR_WARN_KEY, v ? "1" : "0");
    } catch {
      /* ignore */
    }
  };

  // Debounce the filter into `query`. `query` drives the SERVER-SIDE direct
  // search (so a host filter reaches full on-disk history, not just the loaded
  // window); the live `filter` still filters decrypted flows client-side.
  useEffect(() => {
    const t = window.setTimeout(() => setQuery(filter.trim()), 250);
    return () => window.clearTimeout(t);
  }, [filter]);

  const poll = useCallback(async () => {
    try {
      const data = await getJSON<MitmFlow[]>(api(projectId, "/mitm/flows"));
      setFlows(data);
      setStatus(`${data.length} flow${data.length === 1 ? "" : "s"}`);
      setStatusErr(false);
    } catch (e) {
      setStatus(`mitm unavailable: ${(e as Error).message}`);
      setStatusErr(true);
    }
    try {
      // With a query, ask the server to search the whole log; else recent tail.
      const qs = query ? `?q=${encodeURIComponent(query)}` : "";
      const d = await getJSON<{ direct: DirectHost[] }>(api(projectId, `/mitm/direct${qs}`));
      setDirect(d.direct || []);
    } catch {
      /* direct is best-effort */
    }
  }, [projectId, query]);

  useEffect(() => {
    if (!mitmUp) return;
    poll();
    const id = window.setInterval(() => visibleRef.current && poll(), 2000);
    return () => window.clearInterval(id);
  }, [mitmUp, poll]);

  // Load the current monitor list so already-monitored hosts render as historical
  // (not with a Monitor button). Kept authoritative by refreshing on mount, on
  // every poll tick, on window focus, and after a monitor action — so a removal
  // made in the Config tab (or via the CLI) is reflected here without a reload.
  const refreshMonitored = useCallback(async () => {
    try {
      const cfg = await getJSON<ConfigView>(api(projectId, "/config"));
      setMonitoredSet(new Set((cfg.monitor_hosts || []).map((h) => h.toLowerCase())));
    } catch {
      /* best-effort */
    }
  }, [projectId]);
  useEffect(() => {
    if (!mitmUp) return;
    refreshMonitored();
    const id = window.setInterval(() => visibleRef.current && refreshMonitored(), 3000);
    const onFocus = () => refreshMonitored();
    window.addEventListener("focus", onFocus);
    return () => {
      window.clearInterval(id);
      window.removeEventListener("focus", onFocus);
    };
  }, [mitmUp, refreshMonitored]);

  const monitorHost = useCallback(
    async (hostPort: string) => {
      const host = hostPort.split(":")[0];
      setMonitoring((m) => ({ ...m, [hostPort]: "busy" }));
      try {
        const cfg = await getJSON<ConfigView>(api(projectId, "/config"));
        const list = new Set(cfg.monitor_hosts || []);
        list.add(host);
        // custom preset so the explicit monitor list is honored (empty = monitor all).
        await postJSON(api(projectId, "/config/apply"), { mitm_preset: "custom", monitor_hosts: [...list] });
        setMonitoring((m) => ({ ...m, [hostPort]: "done" }));
        refreshMonitored();
      } catch (e) {
        setMonitoring((m) => {
          const n = { ...m };
          delete n[hostPort];
          return n;
        });
        alert(`couldn't start monitoring ${host}: ${(e as Error).message}`);
      }
    },
    [projectId],
  );

  // Gate the Monitor action behind the caveat modal until acknowledged.
  const requestMonitor = useCallback(
    (hostPort: string) => {
      if (ack) {
        monitorHost(hostPort);
        return;
      }
      setUnderstood(false); // re-gate each time the modal opens
      setConfirmHost(hostPort);
    },
    [ack, monitorHost],
  );

  if (!mitmUp) return <p className="empty">Credential proxy isn't running for this project.</p>;

  // Build the merged, newest-first row list. Each direct-dialed request is its
  // OWN row, interleaved with decrypted flows by timestamp — not grouped by host
  // or sunk to the bottom.
  const terms = filter.trim().toLowerCase().split(/\s+/).filter(Boolean);
  const rows: Row[] = [];
  // Decrypted flows: filtered client-side over mitmweb's live set.
  for (const f of flows) {
    const hay = [f.request?.method, f.request?.pretty_host || f.request?.host, f.request?.path, f.response?.status_code]
      .join(" ")
      .toLowerCase();
    if (terms.length && !terms.every((t) => hay.includes(t))) continue;
    rows.push({ kind: "flow", ts: f.request?.timestamp_start || f.timestamp_created || 0, flow: f });
  }
  // Direct rows: the server already applied any host query (?q=), so include all
  // returned entries as-is. Each gets a stable key (host + when + index).
  direct.forEach((dh, i) => {
    rows.push({ kind: "direct", ts: dh.ts, host: dh.host, when: dh.when, key: `${dh.host}|${dh.when}|${i}` });
  });
  rows.sort((a, b) => b.ts - a.ts);
  const shown = rows;

  return (
    <>
      <div className="mitm-toolbar">
        <input type="text" placeholder="filter: host, path, method, status…" autoComplete="off" spellCheck={false} value={filter} onChange={(e) => setFilter(e.target.value)} />
        <span className={statusErr ? "s-4xx" : "muted"}>{status}</span>
      </div>
      <p className="muted cfg-note" style={{ padding: "0 0.5rem 0.5rem" }}>
        Decrypted flows (monitored hosts) and individual direct-dialed requests are interleaved by time. Direct rows are dimmed with a
        “not decrypted” badge — their TLS isn’t intercepted, so contents aren’t available; click <em>Monitor</em> to decrypt future
        requests to that host (the already-completed one can’t be retroactively decrypted). Filtering by host searches the full proxy-log
        history for direct requests; decrypted flows filter over mitmweb’s current set.
      </p>
      <div id="mitm-flows">
        {rows.length === 0 && <p className="empty">No traffic captured yet.</p>}
        {rows.length > 0 && shown.length === 0 && <p className="empty">No flows match “{filter}”.</p>}
        {shown.length > 0 && (
          <table className="mitm-table">
            <thead>
              <tr>
                <th>When</th>
                <th></th>
                <th>Method</th>
                <th>Host</th>
                <th>Path</th>
                <th>Status</th>
                <th>Size</th>
                <th>Dur</th>
              </tr>
            </thead>
            <tbody>
              {shown.map((r) => {
                if (r.kind === "direct") {
                  const mon = monitoring[r.host];
                  // monitoredSet (from live /config) is the source of truth — a host
                  // shows "now monitored" iff it's ACTUALLY in the monitor list now.
                  // The session `monitoring` map only drives the transient "busy"
                  // state; it must NOT keep a row labelled monitored after removal.
                  const nowMonitored = monitoredSet.has(r.host.split(":")[0].toLowerCase());
                  return (
                    <tr key={r.key} className="m-row m-direct" style={{ opacity: 0.62 }}>
                      {/* Show local time from the parsed epoch (like flows) so
                          direct + decrypted rows read in the same zone; fall
                          back to the raw stamp if it didn't parse. */}
                      <td className="m-when">{r.ts ? fmtWhenEpoch(r.ts) : fmtWhenLog(r.when)}</td>
                      <td className="m-caret" />
                      <td className="m-method">—</td>
                      <td className="m-host">
                        <div className="cell-scroll">{r.host}</div>
                      </td>
                      <td className="m-path">
                        <span className="cfg-ssh-badge">not decrypted</span>
                      </td>
                      <td className="m-status">—</td>
                      <td className="m-size">—</td>
                      <td className="m-dur">
                        {nowMonitored ? (
                          <span className="s-2xx" title="This host is now monitored — this was an earlier direct-dialed request; newer ones are decrypted">
                            now monitored
                          </span>
                        ) : (
                          <button className="cfg-btn cfg-btn-ghost" disabled={mon === "busy"} title="Decrypt future requests to this host" onClick={() => requestMonitor(r.host)}>
                            {mon === "busy" ? "…" : "Monitor"}
                          </button>
                        )}
                      </td>
                    </tr>
                  );
                }
                const f = r.flow;
                const req = f.request || {};
                const resp = f.response || {};
                const code = resp.status_code || 0;
                const size = resp.contentLength != null ? resp.contentLength : req.contentLength;
                const open = !!expanded[f.id];
                return (
                  <Fragment key={f.id}>
                    <tr className="m-row" onClick={() => setExpanded((e) => ({ ...e, [f.id]: !e[f.id] }))}>
                      <td className="m-when">{fmtWhenEpoch(req.timestamp_start || f.timestamp_created)}</td>
                      <td className="m-caret">{open ? "▾" : "▸"}</td>
                      <td className="m-method">{req.method || ""}</td>
                      <td className="m-host">
                        <div className="cell-scroll">{req.pretty_host || req.host || ""}</div>
                      </td>
                      <td className="m-path" title={req.path || ""}>
                        <div className="cell-scroll">{req.path || ""}</div>
                      </td>
                      <td className={`m-status ${statusClass(code)}`}>{code || "…"}</td>
                      <td className="m-size">{fmtBytes(size)}</td>
                      <td className="m-dur">{fmtDuration(f)}</td>
                    </tr>
                    {open && (
                      <tr className="m-detailrow">
                        <td colSpan={8}>
                          <div className="mitm-detail">
                            <div className="mitm-sec">Request headers</div>
                            <HeaderTable headers={req.headers} />
                            <div className="mitm-sec">Request body</div>
                            <BodySlot projectId={projectId} flowId={f.id} side="request" msg={req} />
                            <div className="mitm-sec">Response headers</div>
                            <HeaderTable headers={resp.headers} />
                            <div className="mitm-sec">Response body</div>
                            <BodySlot projectId={projectId} flowId={f.id} side="response" msg={resp} />
                          </div>
                        </td>
                      </tr>
                    )}
                  </Fragment>
                );
              })}
            </tbody>
          </table>
        )}
      </div>

      {confirmHost && (
        <Modal title="Monitor this host?" onClose={() => setConfirmHost(null)}>
          <p className="mitm-tldr">
            <strong>TL;DR:</strong> this will most likely just work. If requests to this host stop showing up here after you monitor it,
            it’s a CA-trust issue — remove it from the monitor list in <em>Config → Capture</em> and it goes back to working (undecrypted).
          </p>
          <div className="cfg-warn" style={{ marginTop: 0 }}>
            <strong>⚠ Monitoring makes the proxy present its own CA.</strong> Clients that pin certificates or statically link their TLS with
            a bundled root store won’t trust it — the handshake fails and the request never completes (so a broken host won’t even appear
            here). If a host breaks after you monitor it, remove it from the monitor list in <em>Config → Capture</em>; it falls back to
            direct-dial (undecrypted) and works again.
          </div>
          <p className="muted" style={{ fontSize: "0.82rem" }}>
            Future requests to <code>{confirmHost.split(":")[0]}</code> will be decrypted. The already-completed request can’t be
            retroactively decrypted.
          </p>

          <details className="mitm-readmore">
            <summary>Read more — how the CA is trusted, and what to do when it isn’t</summary>
            <div className="mitm-readmore-body">
              <p>
                The sandbox already trusts the proxy CA for most tooling: it installs the CA into the system trust store and exports{" "}
                <code>SSL_CERT_FILE</code>, <code>REQUESTS_CA_BUNDLE</code>, <code>NODE_EXTRA_CA_CERTS</code>, and{" "}
                <code>CURL_CA_BUNDLE</code> — so Python, Node, curl, and anything using the OS store or those variables work.
              </p>
              <p>
                <strong>Docker-in-Docker &amp; docker builds are handled too:</strong> a cert-injector daemon adds the CA to every inner
                container, and builds read it from <code>/etc/docker/certs.d/&lt;host&gt;/ca.crt</code>.
              </p>
              <p>
                <strong>If a runtime has its own trust store,</strong> point it at the CA the same way — most respect an env var, e.g.{" "}
                <code>SSL_CERT_FILE</code> / <code>SSL_CERT_DIR</code> (Go, OpenSSL), <code>NODE_EXTRA_CA_CERTS</code> (Node),{" "}
                <code>REQUESTS_CA_BUNDLE</code> (Python requests), <code>GIT_SSL_CAINFO</code> (git). The CA is at{" "}
                <code>~/.mitmproxy/mitmproxy-ca-cert.pem</code>.
              </p>
              <p>
                <strong>Statically-linked binaries that embed their own root store</strong> ignore both the system store and those env
                vars — there’s no way to make them trust the proxy CA. For those, don’t monitor the host; instead route just that binary’s
                traffic through an internal proxy of your own that re-terminates and forwards the payloads, or leave it direct-dialed
                (undecrypted).
              </p>
            </div>
          </details>

          {/* "I understand" GATES the action — the button stays disabled until it's
              checked, so the consequences must be acknowledged before monitoring.
              "Don't show again" is separate and persisted (suppresses the modal). */}
          <label className="cfg-inline" style={{ margin: "0.6rem 0 0.3rem" }}>
            <input type="checkbox" checked={understood} onChange={(e) => setUnderstood(e.target.checked)} /> I understand the consequences
          </label>
          <label className="cfg-inline" style={{ margin: "0 0 0.9rem" }}>
            <input type="checkbox" checked={ack} onChange={(e) => persistAck(e.target.checked)} disabled={!understood} /> Don’t show this
            again
          </label>
          <div className="form-actions">
            <button
              className="btn primary"
              disabled={!understood}
              onClick={() => {
                const h = confirmHost;
                setConfirmHost(null);
                monitorHost(h);
              }}
            >
              Monitor host
            </button>
            <button className="cfg-btn cfg-btn-ghost" onClick={() => setConfirmHost(null)}>
              Cancel
            </button>
          </div>
        </Modal>
      )}
    </>
  );
}
