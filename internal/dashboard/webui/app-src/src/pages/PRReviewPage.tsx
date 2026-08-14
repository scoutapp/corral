import { useEffect, useRef, useState } from "react";
import { Link } from "../router";
import { getJSON, postJSON, postRaw, wsURL } from "../api/client";
import type { CachedRepo, PrItem } from "../api/types";
import { useBodyClass } from "../hooks/useBodyClass";
import { ChatPanel } from "../components/ChatPanel";
import { NewProjectModal } from "./ReposModals";
import { renderMarkdown } from "../lib/markdown";
import {
  AnalysisStatusBanner,
  BlockCarousel,
  LinkedPRs,
  PRFilesForensics,
  RiskCard,
  clearFileStatsCache,
} from "./RepoPage";

// PRReviewPage is the dedicated full-page PR review at /repos/<id>/prs/<number>
// (the reference's PRView, not an inline popout). Navigating here Views the PR
// (fetch diff + extract hotness-ranked blocks, no AI) if it isn't already, then
// renders the full-width block carousel with its risk card, file forensics,
// chat, and linked-PRs panels. AI enrichment is an explicit action in the
// carousel.
// PRActions is the write-action bar at the top of the PR page: approve,
// comment, request changes, merge — thin `gh` wrappers. Each opens an inline
// panel (body / merge-method) and confirms before submitting, since all of
// these write to GitHub.
type ActionKind = "approve" | "comment" | "request-changes" | "merge" | null;
function PRActions({ prId }: { prId: number }) {
  const [open, setOpen] = useState<ActionKind>(null);
  const [body, setBody] = useState("");
  const [method, setMethod] = useState("squash");
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState<{ text: string; err: boolean } | null>(null);

  const submit = (kind: Exclude<ActionKind, null>) => {
    setBusy(true);
    setMsg(null);
    const payload =
      kind === "merge" ? { method } : kind === "approve" ? { body } : { body };
    postJSON(`/prs/${prId}/${kind}`, payload)
      .then(() => {
        setMsg({ text: labelFor(kind) + " ✓", err: false });
        setOpen(null);
        setBody("");
      })
      .catch((e) => setMsg({ text: (e as Error).message, err: true }))
      .finally(() => setBusy(false));
  };

  const toggle = (kind: Exclude<ActionKind, null>) => {
    setMsg(null);
    setOpen((cur) => (cur === kind ? null : kind));
  };

  return (
    <div className="pr-actions">
      <div className="pr-actions-row">
        <button type="button" className={`btn${open === "approve" ? " primary" : ""}`} onClick={() => toggle("approve")}>
          ✓ Approve
        </button>
        <button type="button" className={`btn${open === "comment" ? " primary" : ""}`} onClick={() => toggle("comment")}>
          💬 Comment
        </button>
        <button type="button" className={`btn${open === "request-changes" ? " primary" : ""}`} onClick={() => toggle("request-changes")}>
          ✗ Request changes
        </button>
        <button type="button" className={`btn${open === "merge" ? " primary" : ""}`} onClick={() => toggle("merge")}>
          ⑃ Merge
        </button>
        {msg && <span className={`pr-actions-msg${msg.err ? " err" : ""}`}>{msg.text}</span>}
      </div>

      {open === "merge" ? (
        <div className="pr-action-panel">
          <span>Merge method:</span>
          <select value={method} onChange={(e) => setMethod(e.target.value)}>
            <option value="squash">Squash and merge</option>
            <option value="merge">Create a merge commit</option>
            <option value="rebase">Rebase and merge</option>
          </select>
          <button type="button" className="btn primary" disabled={busy} onClick={() => submit("merge")}>
            {busy ? "Merging…" : "Confirm merge"}
          </button>
          <span className="pr-action-warn">This merges the PR on GitHub.</span>
        </div>
      ) : open ? (
        <div className="pr-action-panel col">
          <textarea
            className="pr-action-body"
            placeholder={
              open === "request-changes"
                ? "Describe the changes you're requesting (required)…"
                : open === "approve"
                  ? "Optional approval note…"
                  : "Comment…"
            }
            value={body}
            onChange={(e) => setBody(e.target.value)}
          />
          <div className="pr-action-panel">
            <button type="button" className="btn primary" disabled={busy} onClick={() => submit(open)}>
              {busy ? "Submitting…" : `Confirm ${labelFor(open).toLowerCase()}`}
            </button>
            <button type="button" className="btn" onClick={() => setOpen(null)}>
              Cancel
            </button>
          </div>
        </div>
      ) : null}
    </div>
  );
}

function labelFor(kind: Exclude<ActionKind, null>): string {
  switch (kind) {
    case "approve":
      return "Approved";
    case "comment":
      return "Commented";
    case "request-changes":
      return "Requested changes";
    case "merge":
      return "Merged";
  }
}

// VerifyLaunch fires a sandbox project to verify the PR, ASYNCHRONOUSLY: it
// creates a project on the PR's branch (tagged with the PR source for the
// two-way back-link), starts it, and auto-submits a verify prompt — WITHOUT
// navigating away. The user stays on the PR page; a link to the new project
// appears when it's ready.
// prPromptVars maps a PR into the {{var}} substitution set shared with the
// server-side template renderer (owner_name/pr_number/etc.). Keep the keys in
// sync with what the backend RunContext emits.
function prPromptVars(pr: PrItem, repoName?: string): Record<string, string> {
  return {
    repo: repoName || "",
    branch: pr.headRef || "",
    pr_number: String(pr.number),
    pr_title: pr.title || "",
    pr_url: pr.githubUrl || "",
  };
}

// renderTemplate substitutes {{var}} placeholders (mirrors the Go
// RenderTemplate: unknown vars blank out).
function renderTemplate(tmpl: string, vars: Record<string, string>): string {
  return tmpl.replace(/\{\{\s*([a-zA-Z_][a-zA-Z0-9_]*)\s*\}\}/g, (_, name) => vars[name] ?? "");
}

// The built-in verify prompt, used when no configured template applies. Kept as
// the client-side default so Verify works before any Automations are set up.
function defaultVerifyPrompt(pr: PrItem): string {
  return (
    `Verify PR #${pr.number} ("${pr.title || ""}") works. You're on its branch. ` +
    `Explore the change, run the relevant tests or the app, and report whether it behaves correctly ` +
    `and any issues you find. The PR is ${pr.githubUrl || ""}.`
  );
}

type PromptPreset = { id: number; name: string; scope: string; template: string };

// VerifyLaunch is a split-button: the main action launches a sandbox project on
// the PR's branch and auto-submits a prompt; the ▾ opens a picker to choose
// which prompt (the repo/global default, a saved preset, or a one-off custom
// edit). This is the first surface of configurable prompts.
function VerifyLaunch({
  repoId,
  pr,
  repoName,
}: {
  repoId: string;
  pr: PrItem;
  repoName?: string;
}) {
  const [state, setState] = useState<"idle" | "launching" | "done" | "err">("idle");
  const [projectId, setProjectId] = useState<string | null>(null);
  const [err, setErr] = useState<string | null>(null);

  // Prompt selection. `template` null means "use the built-in verify prompt".
  const [menuOpen, setMenuOpen] = useState(false);
  const [source, setSource] = useState<string>("default");
  const [presets, setPresets] = useState<PromptPreset[]>([]);
  const [template, setTemplate] = useState<string | null>(null);
  const [customizing, setCustomizing] = useState(false);
  const [customText, setCustomText] = useState("");

  // "Build with AI" prompt-drafting (host claude, not sandboxed — read-only).
  const [aiIntent, setAiIntent] = useState("");
  const [aiDrafting, setAiDrafting] = useState(false);
  const [aiLog, setAiLog] = useState("");
  const aiWsRef = useRef<WebSocket | null>(null);
  useEffect(() => () => aiWsRef.current?.close(), []);

  const draftWithAI = () => {
    if (!aiIntent.trim()) return;
    aiWsRef.current?.close();
    setAiDrafting(true);
    setAiLog("");
    const ws = new WebSocket(wsURL(`/api/prompts/draft?repoId=${encodeURIComponent(repoId)}`));
    aiWsRef.current = ws;
    ws.onopen = () => ws.send(JSON.stringify({ description: aiIntent }));
    ws.onmessage = (ev) => {
      let m: Record<string, unknown>;
      try {
        m = JSON.parse(ev.data);
      } catch {
        return;
      }
      if (m.type === "text") setAiLog((l) => l + (m.text as string));
      else if (m.type === "tool_use") setAiLog((l) => l + `› ${m.tool}\n`);
      else if (m.type === "error") setAiLog((l) => l + `\n⚠ ${(m.text as string) || ""}`);
      else if (m.type === "draft" && m.result) {
        setCustomText(m.result as string); // fill the editor with the drafted template
      }
    };
    ws.onclose = () => {
      setAiDrafting(false);
      aiWsRef.current = null;
    };
    ws.onerror = () => setAiLog((l) => l + "\n⚠ draft connection failed");
  };

  // Load the effective verify prompt + project-start presets once, lazily when
  // the menu first opens. The default template resolves from the editable
  // "pr.verify" catalog prompt (built-in → global → repo), so an override in the
  // Prompts carousel changes the launch prompt here too.
  useEffect(() => {
    if (!menuOpen || presets.length || template !== null) return;
    // The pr.verify effective template (honors overrides).
    getJSON<{ prompts: { key: string; effective: string; source: string }[] }>(
      `/api/prompts?repo=${encodeURIComponent(repoId)}`,
    )
      .then((d) => {
        const v = (d.prompts || []).find((p) => p.key === "pr.verify");
        if (v) {
          setSource(v.source);
          setTemplate(v.effective);
        }
      })
      .catch(() => {});
    // Prompt-template presets (saved claude_prompt actions) to pick instead.
    getJSON<{ presets: PromptPreset[] }>(`/api/prompts/project-start?repo=${encodeURIComponent(repoId)}`)
      .then((d) => setPresets(d.presets || []))
      .catch(() => {});
  }, [menuOpen, repoId, presets.length, template]);

  const effectivePrompt = (): string => {
    if (customizing && customText.trim()) return customText;
    if (template) return renderTemplate(template, prPromptVars(pr, repoName));
    return defaultVerifyPrompt(pr);
  };

  const launch = async () => {
    setMenuOpen(false);
    setState("launching");
    setErr(null);
    try {
      const source = {
        kind: "pr",
        repo_id: repoId,
        number: pr.number,
        url: pr.githubUrl,
        title: pr.title,
      };
      const created = await postJSON<{ id: string }>("/projects/create", {
        mode: "clone",
        name: `${repoName || "pr"}-verify-${pr.number}`,
        repos: [{ repoId, branch: pr.headRef || undefined }],
        source,
      });
      const id = created.id;
      setProjectId(id);
      // Start (best-effort) and auto-submit the chosen prompt — no navigation.
      await postJSON(`/p/${id}/start`).catch(() => {});
      await postRaw(`/p/${id}/populate-prompt`, { prompt: effectivePrompt(), submit: true }).catch(() => {});
      setState("done");
    } catch (e) {
      setErr((e as Error).message);
      setState("err");
    }
  };

  if (state === "done" && projectId) {
    return (
      <a className="dock-toggle" href={`/p/${projectId}/`} title="Open the verify project">
        ✓ verifying → open project
      </a>
    );
  }

  const label =
    state === "launching" ? "Launching…" : state === "err" ? `⚠ ${err}` : "▶ Verify in sandbox";
  const promptLabel = customizing
    ? "custom prompt"
    : template
      ? `${source} prompt`
      : "default verify prompt";

  return (
    <div className="split-btn">
      <button
        type="button"
        className="dock-toggle split-main"
        disabled={state === "launching"}
        title="Create a sandbox project on this PR's branch and auto-start Claude to verify it (runs in the background)"
        onClick={launch}
      >
        {label}
      </button>
      <button
        type="button"
        className="dock-toggle split-caret"
        disabled={state === "launching"}
        title="Choose which prompt to launch with"
        aria-label="Choose prompt"
        onClick={() => setMenuOpen((o) => !o)}
      >
        ▾
      </button>

      {menuOpen && (
        <div className="split-menu" role="menu">
          <div className="split-menu-head">Launch prompt · {promptLabel}</div>
          <button
            type="button"
            className={`split-menu-item${!template && !customizing ? " active" : ""}`}
            onClick={() => {
              setTemplate(null);
              setCustomizing(false);
            }}
          >
            Built-in verify prompt
          </button>
          {presets.map((p) => (
            <button
              key={p.id}
              type="button"
              className={`split-menu-item${template === p.template && !customizing ? " active" : ""}`}
              onClick={() => {
                setTemplate(p.template);
                setCustomizing(false);
              }}
            >
              {p.name} <span className="split-menu-scope">{p.scope}</span>
            </button>
          ))}
          <div className="split-menu-sep" />
          <button
            type="button"
            className={`split-menu-item${customizing ? " active" : ""}`}
            onClick={() => {
              setCustomizing(true);
              if (!customText) setCustomText(effectivePrompt());
            }}
          >
            ✎ Customize prompt…
          </button>
          {customizing && (
            <>
              <textarea
                className="split-menu-textarea"
                rows={6}
                value={customText}
                onChange={(e) => setCustomText(e.target.value)}
                placeholder="Prompt to send when the sandbox starts…"
              />
              <div className="split-menu-ai">
                <div className="split-menu-ai-head">
                  ✨ Build with AI
                  <span className="ai-warn" title="Runs your host machine's Claude, not the sandbox; read-only tools">
                    host · not sandboxed · read-only
                  </span>
                </div>
                <textarea
                  className="split-menu-textarea"
                  rows={2}
                  value={aiIntent}
                  onChange={(e) => setAiIntent(e.target.value)}
                  placeholder="Describe what this prompt should do — e.g. 'review the diff, run the test suite, and summarize risks'"
                />
                <button
                  type="button"
                  className="dock-toggle"
                  style={{ marginLeft: 0 }}
                  disabled={aiDrafting || !aiIntent.trim()}
                  onClick={draftWithAI}
                >
                  {aiDrafting ? "Drafting…" : "Draft with AI"}
                </button>
                {aiLog && <pre className="split-menu-ai-log">{aiLog}</pre>}
              </div>
            </>
          )}
        </div>
      )}
    </div>
  );
}

// PRDescription renders the PR's markdown body, collapsed by default when long
// (GitHub descriptions can be huge — templates, checklists) with a show-more.
function PRDescription({ body }: { body?: string }) {
  const [open, setOpen] = useState(false);
  const text = (body || "").trim();
  if (!text) return null;
  const long = text.length > 600;
  return (
    <div className={`pr-desc${long && !open ? " clamped" : ""}`}>
      <div className="pr-desc-head">Description</div>
      <div
        className="pr-desc-body markdown"
        dangerouslySetInnerHTML={{ __html: renderMarkdown(text) }}
      />
      {long && (
        <button type="button" className="pr-desc-more" onClick={() => setOpen((v) => !v)}>
          {open ? "Show less" : "Show more"}
        </button>
      )}
    </div>
  );
}

export function PRReviewPage({ repoId, number }: { repoId: string; number: number }) {
  useBodyClass("console");
  const [repo, setRepo] = useState<CachedRepo | null>(null);
  const [pr, setPr] = useState<PrItem | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [nonce, setNonce] = useState(0); // bumped after re-analyze to remount panels
  const [chatOpen, setChatOpen] = useState(false);
  const [startOpen, setStartOpen] = useState(false);
  const [linksOpen, setLinksOpen] = useState(false);
  const [refreshing, setRefreshing] = useState(false);

  // Force a re-pull from GitHub: re-fetches the PR (body + diff) and re-extracts
  // blocks. Use when the stored copy is stale (e.g. the PR was updated, or was
  // fetched before a new field like the description existed).
  const refresh = () => {
    setRefreshing(true);
    setErr(null);
    postJSON<{ pr: PrItem }>(`/repos/${encodeURIComponent(repoId)}/prs/fetch`, { number })
      .then((r) => {
        setPr(r.pr);
        clearFileStatsCache(r.pr.id);
        setNonce((n) => n + 1);
      })
      .catch((e) => setErr((e as Error).message))
      .finally(() => setRefreshing(false));
  };

  useEffect(() => {
    getJSON<{ repos: CachedRepo[] }>("/repos")
      .then((d) => setRepo((d.repos || []).find((r) => r.id === repoId) || null))
      .catch(() => {});
  }, [repoId]);

  // Resolve the PR to its stored record. It may already be viewed (in /prs); if
  // not, View it (idempotent fetch upserts + extracts blocks without AI).
  useEffect(() => {
    let live = true;
    getJSON<{ prs: PrItem[] }>(`/repos/${encodeURIComponent(repoId)}/prs`)
      .then((d) => {
        const existing = (d.prs || []).find((p) => p.number === number);
        if (existing) {
          if (live) setPr(existing);
          return;
        }
        return postJSON<{ pr: PrItem }>(`/repos/${encodeURIComponent(repoId)}/prs/fetch`, {
          number,
        }).then((r) => live && setPr(r.pr));
      })
      .catch((e) => live && setErr((e as Error).message));
    return () => {
      live = false;
    };
  }, [repoId, number]);

  return (
    <>
      <header className="console-header">
        <div className="brand">
          <Link to={`/repos/${encodeURIComponent(repoId)}`} className="back">
            ← {repo ? repo.name : "repo"}
          </Link>
          <span className="brand-name">
            #{number} {pr?.shortSummary || pr?.title || ""}
          </span>
          {pr?.state && <span className="pr-state">{pr.state}</span>}
          {pr?.githubUrl && (
            <a className="brand-sub" href={pr.githubUrl} target="_blank" rel="noreferrer">
              view on GitHub ↗
            </a>
          )}
        </div>
        {pr && (
          <>
            <button
              type="button"
              className="dock-toggle"
              disabled={refreshing}
              title="Re-pull this PR from GitHub (description, diff) and re-extract blocks"
              onClick={refresh}
            >
              {refreshing ? "Refreshing…" : "↻ Refresh from GitHub"}
            </button>
            <button
              type="button"
              className={`dock-toggle${linksOpen ? " on" : ""}`}
              title="Link this PR to related PRs"
              onClick={() => setLinksOpen((v) => !v)}
            >
              🔗 Linked PRs
            </button>
            <VerifyLaunch repoId={repoId} pr={pr} repoName={repo?.name} />
            <button
              type="button"
              className="dock-toggle"
              title="Start a sandbox project on this PR's branch (opens the create dialog)"
              onClick={() => setStartOpen(true)}
            >
              ⧉ Start project
            </button>
            <button
              type="button"
              className={`dock-toggle${chatOpen ? " on" : ""}`}
              title="Ask Claude about this PR — a host claude chat"
              onClick={() => setChatOpen((v) => !v)}
            >
              💬 Ask Claude
            </button>
          </>
        )}
      </header>

      <div className="pr-review-page">
        {pr && linksOpen && (
          <div className="pr-links-panel">
            <LinkedPRs prId={pr.id} />
          </div>
        )}
        {pr && <PRActions prId={pr.id} />}
        {err ? (
          <p className="tab-note err">Failed to load PR #{number}: {err}</p>
        ) : !pr ? (
          <p className="tab-note">Loading PR #{number}…</p>
        ) : (
          <>
            <AnalysisStatusBanner
              repoId={repoId}
              onAnalyzed={() => {
                clearFileStatsCache(pr.id);
                setNonce((n) => n + 1);
              }}
            />
            <PRDescription body={pr.body} />
            {/* Top: Risk (left) + Files changed (right); stacks Risk-first when
                too narrow. Then the block carousel full-width below. */}
            <div className="pr-top">
              <div className="pr-top-left">
                <RiskCard key={`risk-${nonce}`} prId={pr.id} />
              </div>
              <div className="pr-top-right">
                <PRFilesForensics key={`ff-${nonce}`} prId={pr.id} />
              </div>
            </div>
            <BlockCarousel key={`bc-${nonce}`} prId={pr.id} />
          </>
        )}
      </div>

      {/* Ask-Claude drawer — reuses the projects-page ChatPanel + chrome,
          scoped to this PR (the server injects PR summary + hot-file context). */}
      {pr && chatOpen && (
        <div className="chat-panel" id="chat-panel">
          <div className="chat-panel-bar">
            <span>
              <i className="screen-dot" />
              ask claude · PR #{number}
            </span>
            <span className="chat-panel-actions">
              <button type="button" title="Close" onClick={() => setChatOpen(false)}>
                ✕
              </button>
            </span>
          </div>
          <div className="chat-panel-iframe">
            <ChatPanel wsPath={`/prs/${pr.id}/chat/ws`} />
          </div>
        </div>
      )}

      {pr && startOpen && (
        <NewProjectModal
          presetRepoId={repoId}
          presetBranch={pr.headRef}
          presetName={`${repo?.name || "pr"}-pr-${number}`}
          onClose={() => setStartOpen(false)}
        />
      )}
    </>
  );
}
