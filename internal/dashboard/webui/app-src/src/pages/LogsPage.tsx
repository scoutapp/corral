import { useCallback, useEffect, useRef, useState } from "react";
import { Link } from "../router";
import { getJSON } from "../api/client";
import { useBodyClass } from "../hooks/useBodyClass";

// LogsPage — the host-wide application log: everything the app does or runs
// (AI analysis, PR actions, project lifecycle, automation runs, scripts, HTTP
// requests). Keyset-paginated (load older), filterable (category/project/level),
// and searchable (free-text over message + meta). Automation rows deep-link to
// the run detail. HTTP request logs are high-volume, so a "hide request logs"
// toggle is on by default — you still get everything, just not buried in polls.

type LogRecord = {
  id: number;
  ts: string;
  level: string;
  category: string;
  event: string;
  message: string;
  repoId?: string;
  projectId?: string;
  status?: string;
  durationMs?: number;
  meta: string; // raw JSON
  runId?: number;
};

const LEVELS = ["", "info", "warn", "error", "debug"];

function levelClass(level: string, status?: string): string {
  if (level === "error" || status === "error") return "log-error";
  if (level === "warn" || status === "partial") return "log-warn";
  return "log-ok";
}

function categoryIcon(cat: string): string {
  switch (cat) {
    case "ai": return "✨";
    case "pr-action": return "⑃";
    case "project": return "▤";
    case "automation": return "⚡";
    case "script": return "⚙";
    case "repo": return "◧";
    case "chat": return "💬";
    case "http": return "→";
    default: return "•";
  }
}

// Short local time (the ts is ISO UTC; show it in the browser's tz).
function fmtTime(ts: string): string {
  const d = new Date(ts);
  if (isNaN(d.getTime())) return ts;
  return d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit" });
}

export function LogsPage() {
  useBodyClass("console");

  const [logs, setLogs] = useState<LogRecord[]>([]);
  const [cursor, setCursor] = useState<number>(0); // next-older cursor; 0 = no more
  const [loading, setLoading] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [open, setOpen] = useState<Record<number, boolean>>({});

  // Filters.
  const [category, setCategory] = useState("");
  const [project, setProject] = useState("");
  const [level, setLevel] = useState("");
  const [q, setQ] = useState("");
  // Off by default: hiding request logs when they're the only activity yields a
  // confusingly empty page. Users can toggle it on to cut the noise.
  const [hideHttp, setHideHttp] = useState(false);

  // Facet options.
  const [categories, setCategories] = useState<string[]>([]);
  const [projects, setProjects] = useState<string[]>([]);
  useEffect(() => {
    getJSON<{ categories: string[]; projects: string[] }>("/api/logs/facets")
      .then((d) => {
        setCategories(d.categories || []);
        setProjects(d.projects || []);
      })
      .catch(() => {});
  }, []);

  // A search debounce so typing doesn't refetch on every keystroke.
  const qDebounce = useRef<number | null>(null);
  const [qApplied, setQApplied] = useState("");
  useEffect(() => {
    if (qDebounce.current) window.clearTimeout(qDebounce.current);
    qDebounce.current = window.setTimeout(() => setQApplied(q.trim()), 300);
    return () => {
      if (qDebounce.current) window.clearTimeout(qDebounce.current);
    };
  }, [q]);

  const params = useCallback(
    (before: number) => {
      const p = new URLSearchParams({ limit: "60" });
      if (before) p.set("before", String(before));
      // "hide request logs" is client intent; when no explicit category is set
      // and hideHttp is on, we can't exclude server-side (only filter-in), so we
      // filter http out client-side after fetching. If a category IS chosen it
      // wins.
      if (category) p.set("category", category);
      if (project) p.set("project", project);
      if (level) p.set("level", level);
      if (qApplied) p.set("q", qApplied);
      return p.toString();
    },
    [category, project, level, qApplied],
  );

  // Load the first page whenever filters change.
  const loadFirst = useCallback(() => {
    setLoading(true);
    setErr(null);
    getJSON<{ logs: LogRecord[]; nextCursor: number }>(`/api/logs?${params(0)}`)
      .then((d) => {
        setLogs(d.logs || []);
        setCursor(d.nextCursor || 0);
      })
      .catch((e) => setErr((e as Error).message))
      .finally(() => setLoading(false));
  }, [params]);

  useEffect(() => {
    loadFirst();
  }, [loadFirst]);

  const loadOlder = () => {
    if (!cursor || loading) return;
    setLoading(true);
    getJSON<{ logs: LogRecord[]; nextCursor: number }>(`/api/logs?${params(cursor)}`)
      .then((d) => {
        setLogs((prev) => [...prev, ...(d.logs || [])]);
        setCursor(d.nextCursor || 0);
      })
      .catch((e) => setErr((e as Error).message))
      .finally(() => setLoading(false));
  };

  // Client-side http hide (only when no explicit category picked).
  const visible = logs.filter((l) => !(hideHttp && !category && l.category === "http"));

  return (
    <>
      <header className="console-header">
        <div className="brand">
          <Link to="/" className="back">
            ← All projects
          </Link>
          <span className="brand-name">Logs</span>
          <button type="button" className="brand-sub auto-btn link" onClick={loadFirst}>
            ⟳ refresh
          </button>
        </div>
      </header>

      <div className="auto-page">
        <div className="logs-filters">
          <input
            className="auto-input logs-search"
            placeholder="🔍 search logs…"
            value={q}
            onChange={(e) => setQ(e.target.value)}
          />
          <select className="auto-input" value={category} onChange={(e) => setCategory(e.target.value)}>
            <option value="">all categories</option>
            {categories.map((c) => (
              <option key={c} value={c}>{c}</option>
            ))}
          </select>
          {projects.length > 0 && (
            <select className="auto-input" value={project} onChange={(e) => setProject(e.target.value)}>
              <option value="">all projects</option>
              {projects.map((p) => (
                <option key={p} value={p}>{p}</option>
              ))}
            </select>
          )}
          <select className="auto-input" value={level} onChange={(e) => setLevel(e.target.value)}>
            {LEVELS.map((l) => (
              <option key={l} value={l}>{l || "all levels"}</option>
            ))}
          </select>
          <label className="logs-hidehttp">
            <input type="checkbox" checked={hideHttp} onChange={(e) => setHideHttp(e.target.checked)} disabled={!!category} />
            hide request logs
          </label>
        </div>

        {err && <div className="auto-msg err">{err}</div>}

        {visible.length === 0 && !loading ? (
          // Distinguish "nothing at all" from "everything is hidden by the
          // request-logs filter" — the latter is a filter state, not empty data.
          logs.length > 0 && hideHttp && !category ? (
            <p className="auto-empty">
              Only request logs so far, and they're hidden.{" "}
              <button type="button" className="auto-btn link" onClick={() => setHideHttp(false)}>
                Show request logs
              </button>
            </p>
          ) : (
            <p className="auto-empty">No logs match. Activity is recorded here as it happens.</p>
          )
        ) : (
          <ul className="logs-list">
            {visible.map((l) => {
              const isOpen = !!open[l.id];
              const hasDetail = (l.meta && l.meta !== "{}") || l.runId;
              return (
                <li key={l.id} className={`logs-row ${levelClass(l.level, l.status)}`}>
                  <button
                    type="button"
                    className="logs-row-head"
                    onClick={() => hasDetail && setOpen((o) => ({ ...o, [l.id]: !o[l.id] }))}
                  >
                    <span className="logs-time">{fmtTime(l.ts)}</span>
                    <span className="logs-cat" title={l.category}>
                      {categoryIcon(l.category)} {l.category}
                    </span>
                    <span className="logs-msg">{l.message}</span>
                    {l.status && <span className={`logs-status ${levelClass(l.level, l.status)}`}>{l.status}</span>}
                    {typeof l.durationMs === "number" && l.durationMs > 0 && (
                      <span className="logs-dur">{l.durationMs}ms</span>
                    )}
                    {hasDetail && <span className="logs-caret">{isOpen ? "▾" : "▸"}</span>}
                  </button>
                  {isOpen && (
                    <div className="logs-detail">
                      <div className="logs-detail-meta">
                        <span className="logs-detail-k">event</span> <code>{l.event}</code>
                        {l.repoId && (<><span className="logs-detail-k">repo</span> <code>{l.repoId}</code></>)}
                        {l.projectId && (<><span className="logs-detail-k">project</span> <code>{l.projectId}</code></>)}
                      </div>
                      {l.meta && l.meta !== "{}" && <pre className="logs-detail-json">{prettyJSON(l.meta)}</pre>}
                      {l.runId ? (
                        <Link to="/automations/runs" className="auto-btn link">
                          View run #{l.runId} →
                        </Link>
                      ) : null}
                    </div>
                  )}
                </li>
              );
            })}
          </ul>
        )}

        <div className="logs-more">
          {cursor ? (
            <button type="button" className="auto-btn" disabled={loading} onClick={loadOlder}>
              {loading ? "Loading…" : "Load older"}
            </button>
          ) : (
            !loading && visible.length > 0 && <span className="auto-empty">— end of logs —</span>
          )}
        </div>
      </div>
    </>
  );
}

function prettyJSON(raw: string): string {
  try {
    return JSON.stringify(JSON.parse(raw), null, 2);
  } catch {
    return raw;
  }
}
