import { Fragment, useCallback, useEffect, useRef, useState } from "react";
import { getJSON, getText, postJSON } from "../api/client";
import type { ConfigView, DirectHost, MitmFlow, MitmMessage } from "../api/types";

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
  const visibleRef = useRef(true);

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
                  return (
                    <tr key={r.key} className="m-row m-direct" style={{ opacity: 0.62 }}>
                      <td className="m-when">{fmtWhenLog(r.when)}</td>
                      <td className="m-caret" />
                      <td className="m-method">—</td>
                      <td className="m-host">{r.host}</td>
                      <td className="m-path">
                        <span className="cfg-ssh-badge">not decrypted</span>
                      </td>
                      <td className="m-status">—</td>
                      <td className="m-size">—</td>
                      <td className="m-dur">
                        {mon === "done" ? (
                          <span className="s-2xx" title="Future requests to this host will be decrypted">
                            monitoring ✓
                          </span>
                        ) : (
                          <button className="cfg-btn cfg-btn-ghost" disabled={mon === "busy"} title="Decrypt future requests to this host" onClick={() => monitorHost(r.host)}>
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
                      <td className="m-host">{req.pretty_host || req.host || ""}</td>
                      <td className="m-path" title={req.path || ""}>
                        {req.path || ""}
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
    </>
  );
}
