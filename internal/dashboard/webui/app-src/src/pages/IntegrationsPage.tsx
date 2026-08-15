import { useCallback, useEffect, useState } from "react";
import { Link } from "../router";
import { getJSON, postJSON, delJSON } from "../api/client";
import { useBodyClass } from "../hooks/useBodyClass";

// IntegrationsPage — connect MCP servers at the HOST. Corral drives the host
// `claude` CLI's native registry (GET/POST/DELETE /api/mcp), so a server
// connected here is available to the dashboard chat too. Host-only: the sandbox
// never reaches these. Managing servers from the browser is never gated — the
// API-writes gate only governs the CLI / host Claude.

type McpServer = {
  name: string;
  url?: string;
  transport?: "http" | "sse" | "stdio";
  status: "connected" | "needs_auth" | "pending" | "unknown";
  statusText?: string;
};

const STATUS_LABEL: Record<McpServer["status"], string> = {
  connected: "connected",
  needs_auth: "needs auth",
  pending: "pending",
  unknown: "unknown",
};

export function IntegrationsPage() {
  useBodyClass("console");

  const [servers, setServers] = useState<McpServer[]>([]);
  const [loading, setLoading] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [msg, setMsg] = useState<string | null>(null);

  // Add form.
  const [showAdd, setShowAdd] = useState(false);
  const [name, setName] = useState("");
  const [transport, setTransport] = useState<"http" | "sse">("http");
  const [url, setUrl] = useState("");
  const [header, setHeader] = useState("");
  const [adding, setAdding] = useState(false);

  const load = useCallback(() => {
    setLoading(true);
    setErr(null);
    getJSON<{ servers: McpServer[] }>("/api/mcp")
      .then((d) => setServers(d.servers || []))
      .catch((e) => setErr((e as Error).message))
      .finally(() => setLoading(false));
  }, []);

  useEffect(() => load(), [load]);

  async function add() {
    if (!name.trim() || !url.trim()) {
      setErr("A name and URL are required.");
      return;
    }
    setAdding(true);
    setErr(null);
    try {
      await postJSON("/api/mcp", { name: name.trim(), transport, url: url.trim(), header: header.trim() });
      setMsg(`Connected ${name.trim()}.`);
      setName("");
      setUrl("");
      setHeader("");
      setShowAdd(false);
      load();
    } catch (e) {
      setErr(`Couldn't connect: ${(e as Error).message}`);
    } finally {
      setAdding(false);
    }
  }

  async function remove(srv: McpServer) {
    if (!confirm(`Remove ${srv.name}? The host claude will no longer connect to it.`)) return;
    setErr(null);
    try {
      await delJSON(`/api/mcp/${encodeURIComponent(srv.name)}`);
      setMsg(`Removed ${srv.name}.`);
      load();
    } catch (e) {
      setErr(`Couldn't remove: ${(e as Error).message}`);
    }
  }

  return (
    <>
      <header className="console-header">
        <div className="brand">
          <Link to="/" className="back">
            ← All projects
          </Link>
          <span className="brand-name">Integrations</span>
          <button type="button" className="brand-sub auto-btn link" onClick={load}>
            ⟳ refresh
          </button>
        </div>
      </header>

      <div className="auto-page">
        <p className="auto-intro">
          Connect MCP servers on the host — the dashboard chat can use them. These live in your host{" "}
          <code>claude</code> setup and are never reachable from a sandbox.
        </p>

        {err && <div className="auto-msg err">{err}</div>}
        {msg && !err && <div className="auto-msg">{msg}</div>}

        {servers.length === 0 && !loading ? (
          <p className="auto-empty">
            No MCP servers connected yet.{" "}
            <button type="button" className="auto-btn link" onClick={() => setShowAdd(true)}>
              Add one
            </button>
          </p>
        ) : (
          <ul className="mcp-list">
            {servers.map((s) => (
              <li key={s.name} className="mcp-row">
                <span className={`mcp-dot mcp-${s.status}`} title={s.statusText || s.status} />
                <div className="mcp-id">
                  <div className="mcp-name">{s.name}</div>
                  <div className="mcp-url">
                    {s.url}
                    {s.transport ? <span className="mcp-transport"> · {s.transport}</span> : null}
                  </div>
                </div>
                <div className="mcp-right">
                  <span className={`mcp-status mcp-${s.status}`}>{STATUS_LABEL[s.status]}</span>
                  {s.status === "needs_auth" && (
                    <span className="mcp-auth-hint" title="Authenticate from your terminal: claude mcp login">
                      authenticate in terminal
                    </span>
                  )}
                  <button type="button" className="mcp-x" title="Remove" onClick={() => remove(s)}>
                    ×
                  </button>
                </div>
              </li>
            ))}
          </ul>
        )}

        {showAdd ? (
          <div className="mcp-add-form">
            <h3 className="mcp-add-h">Add an MCP server</h3>
            <div className="mcp-field">
              <label>Name</label>
              <input className="auto-input" placeholder="sentry" value={name} onChange={(e) => setName(e.target.value)} />
            </div>
            <div className="mcp-field">
              <label>Transport</label>
              <select className="auto-input" value={transport} onChange={(e) => setTransport(e.target.value as "http" | "sse")}>
                <option value="http">http</option>
                <option value="sse">sse</option>
              </select>
            </div>
            <div className="mcp-field">
              <label>URL</label>
              <input className="auto-input" placeholder="https://mcp.sentry.dev/mcp" value={url} onChange={(e) => setUrl(e.target.value)} />
            </div>
            <div className="mcp-field">
              <label>Authorization</label>
              <input
                className="auto-input"
                placeholder="Bearer …  (optional — leave blank to authenticate later)"
                value={header}
                onChange={(e) => setHeader(e.target.value ? headerValue(e.target.value) : "")}
              />
            </div>
            <div className="mcp-add-actions">
              <button type="button" className="auto-btn" disabled={adding} onClick={add}>
                {adding ? "Connecting…" : "Connect"}
              </button>
              <button type="button" className="auto-btn link" onClick={() => setShowAdd(false)}>
                Cancel
              </button>
            </div>
          </div>
        ) : (
          servers.length > 0 && (
            <button type="button" className="mcp-add-btn" onClick={() => setShowAdd(true)}>
              + Add MCP server
            </button>
          )
        )}
      </div>
    </>
  );
}

// headerValue lets the user type just the token ("Bearer xyz" or "xyz") while we
// send the full "Authorization: <value>" the CLI expects. If they already typed a
// header name (contains ": "), pass it through untouched.
function headerValue(raw: string): string {
  const v = raw.trim();
  if (v.includes(": ")) return v; // already "Header: value"
  return `Authorization: ${v}`;
}
