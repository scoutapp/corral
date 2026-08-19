import { useCallback, useEffect, useRef, useState } from "react";
import { Link } from "../router";
import { getJSON } from "../api/client";
import { useBodyClass } from "../hooks/useBodyClass";
import { ConversationsPanel } from "../components/ConversationsPanel";

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
  traceId?: string;
  spanId?: string;
  parentSpanId?: string;
};

// A reconciled span from GET /api/logs/trace/:id.
type TraceSpan = {
  spanId: string;
  parentSpanId?: string;
  category: string;
  event: string;
  message: string;
  level: string;
  status?: string;
  startTs: string;
  endTs?: string;
  durationMs: number;
  unterminated?: boolean;
  repoId?: string;
  projectId?: string;
  runId?: number;
  meta?: string;
  children?: TraceSpan[];
};

type TraceTree = {
  traceId: string;
  roots: TraceSpan[];
  spanCount: number;
  rowCount: number;
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

// AppLogsPanel is the application activity log (the original Logs page body): a
// keyset-paginated, filterable, searchable log with an inline trace waterfall.
// It's the "Logs" tab of the LogsPage shell below.
function AppLogsPanel() {
  const [logs, setLogs] = useState<LogRecord[]>([]);
  const [cursor, setCursor] = useState<number>(0); // next-older cursor; 0 = no more
  const [loading, setLoading] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [open, setOpen] = useState<Record<number, boolean>>({});
  // The trace_id whose waterfall is expanded inline (only one at a time), plus
  // its fetched tree and load state.
  const [openTrace, setOpenTrace] = useState<string | null>(null);
  const [trace, setTrace] = useState<TraceTree | null>(null);
  const [traceLoading, setTraceLoading] = useState(false);

  const toggleTrace = useCallback((traceId: string) => {
    setOpenTrace((cur) => {
      if (cur === traceId) {
        setTrace(null);
        return null;
      }
      setTrace(null);
      setTraceLoading(true);
      getJSON<TraceTree>(`/api/logs/trace/${encodeURIComponent(traceId)}`)
        .then((t) => setTrace(t))
        .catch(() => setTrace(null))
        .finally(() => setTraceLoading(false));
      return traceId;
    });
  }, []);

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
    <div className="auto-page">
      {/* A small refresh affordance stays inside the panel (the shell header owns
          the title + tab bar). */}
      <div className="logs-panel-toolbar">
        <button type="button" className="auto-btn link" onClick={loadFirst}>
          ⟳ refresh
        </button>
      </div>
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
              const hasDetail = (l.meta && l.meta !== "{}") || l.runId || l.traceId;
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
                      <div className="logs-detail-actions">
                        {l.runId ? (
                          <Link to="/automations/runs" className="auto-btn link">
                            View run #{l.runId} →
                          </Link>
                        ) : null}
                        {l.traceId ? (
                          <button
                            type="button"
                            className="auto-btn link"
                            onClick={() => toggleTrace(l.traceId!)}
                          >
                            {openTrace === l.traceId ? "Hide trace ▾" : "View trace ▸"}
                          </button>
                        ) : null}
                      </div>
                      {l.traceId && openTrace === l.traceId && (
                        <TraceView loading={traceLoading} trace={trace} highlight={l.spanId} />
                      )}
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
  );
}

// LogsPage is the shell: a header with two tabs — the application activity Log
// and the captured Conversations — switching between AppLogsPanel and
// ConversationsPanel. The /logs route renders this.
export function LogsPage() {
  useBodyClass("console");
  const [tab, setTab] = useState<"logs" | "conversations">("logs");
  return (
    <>
      <header className="console-header">
        <div className="brand">
          <Link to="/" className="back">
            ← All projects
          </Link>
          <span className="brand-name">Logs</span>
        </div>
        <div className="logs-tabs">
          <button
            type="button"
            className={`dock-toggle${tab === "logs" ? " on" : ""}`}
            onClick={() => setTab("logs")}
          >
            Activity log
          </button>
          <button
            type="button"
            className={`dock-toggle${tab === "conversations" ? " on" : ""}`}
            onClick={() => setTab("conversations")}
          >
            Conversations
          </button>
        </div>
      </header>
      {tab === "logs" ? <AppLogsPanel /> : <ConversationsPanel />}
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

// A flattened span with its depth, for rendering the waterfall as rows.
type FlatSpan = { span: TraceSpan; depth: number };

function flatten(spans: TraceSpan[] | undefined, depth: number, out: FlatSpan[]) {
  for (const s of spans || []) {
    out.push({ span: s, depth });
    flatten(s.children, depth + 1, out);
  }
}

function tsMs(ts?: string): number {
  if (!ts) return NaN;
  const d = new Date(ts).getTime();
  return isNaN(d) ? NaN : d;
}

// TraceView draws the causal waterfall for one trace: each span a labeled row
// with a bar positioned by its start/end within the trace's overall window.
// Spans nest by indent; the row the user opened from is highlighted.
function TraceView({ loading, trace, highlight }: { loading: boolean; trace: TraceTree | null; highlight?: string }) {
  if (loading) return <div className="trace-view loading">Loading trace…</div>;
  if (!trace || trace.roots.length === 0) return <div className="trace-view empty">No trace data.</div>;

  const flat: FlatSpan[] = [];
  flatten(trace.roots, 0, flat);

  // Trace window: earliest start → latest end across all spans. Fall back to a
  // 1ms window so a single instantaneous span still renders a visible bar.
  let t0 = Infinity;
  let t1 = -Infinity;
  for (const { span } of flat) {
    const s = tsMs(span.startTs);
    const e = tsMs(span.endTs) || s;
    if (!isNaN(s)) t0 = Math.min(t0, s);
    if (!isNaN(e)) t1 = Math.max(t1, e);
  }
  if (!isFinite(t0) || !isFinite(t1)) {
    t0 = 0;
    t1 = 1;
  }
  const span = Math.max(1, t1 - t0);

  return (
    <div className="trace-view">
      <div className="trace-view-top">
        <span className="trace-view-id">trace {trace.traceId.slice(0, 8)}…</span>
        <span className="trace-view-count">
          {trace.spanCount} span{trace.spanCount === 1 ? "" : "s"} · {trace.rowCount} rows
        </span>
      </div>
      <div className="trace-view-rows">
        {flat.map(({ span: s, depth }, i) => {
          const start = tsMs(s.startTs);
          const end = tsMs(s.endTs) || start;
          const left = isNaN(start) ? 0 : ((start - t0) / span) * 100;
          const width = isNaN(start) ? 100 : Math.max(1.5, ((end - start) / span) * 100);
          const barClass =
            s.status === "error" || s.level === "error"
              ? "err"
              : s.unterminated
                ? "open"
                : `cat-${s.category}`;
          return (
            <div
              key={s.spanId + i}
              className={`trace-span${s.spanId === highlight ? " hi" : ""}`}
            >
              <div className="trace-label" style={{ paddingLeft: `${depth * 14}px` }} title={s.event}>
                <span className="trace-glyph">{categoryIcon(s.category)}</span>
                <span className="trace-event">{s.event}</span>
                <span className="trace-message">{s.message}</span>
              </div>
              <div className="trace-track">
                <div
                  className={`trace-bar ${barClass}`}
                  style={{ left: `${left}%`, width: `${Math.min(width, 100 - left)}%` }}
                >
                  <span className="trace-bar-dur">
                    {s.unterminated ? "…" : s.durationMs > 0 ? `${s.durationMs}ms` : ""}
                  </span>
                </div>
              </div>
            </div>
          );
        })}
      </div>
    </div>
  );
}
