import { useEffect, useRef, useState } from "react";
import { Link } from "../router";
import { getJSON, postJSON, postRaw, delJSON, wsURL } from "../api/client";
import type { CachedRepo, LinkedIssue, PrItem } from "../api/types";
import { useBodyClass } from "../hooks/useBodyClass";
import { ChatPanel } from "../components/ChatPanel";
import { AgentContextStaleBanner } from "../components/AgentContextStaleBanner";
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
type ActionKind = "approve" | "comment" | "request-changes" | null;
function PRActions({ prId, repoId, pr, repoName }: { prId: number; repoId: string; pr: PrItem; repoName?: string }) {
  const [open, setOpen] = useState<ActionKind>(null);
  const [body, setBody] = useState("");
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState<{ text: string; err: boolean } | null>(null);

  const submit = (kind: Exclude<ActionKind, null>) => {
    setBusy(true);
    setMsg(null);
    postJSON(`/prs/${prId}/${kind}`, { body })
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
        <MergeControl prId={prId} repoId={repoId} pr={pr} repoName={repoName} />
        {msg && <span className={`pr-actions-msg${msg.err ? " err" : ""}`}>{msg.text}</span>}
      </div>

      {open ? (
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
  }
}

// MergeMode is which execution path the Merge split-button runs.
type MergeMode = "sandbox" | "host" | "plain";

// mergeModeLabel is the human name shown on the button / menu for each mode.
function mergeModeLabel(m: MergeMode): string {
  return m === "host" ? "Merge with host" : m === "sandbox" ? "Merge with sandbox" : "Merge";
}

// MergeStrategyState is what /repos/<id>/merge-strategy returns.
type MergeStrategyState = {
  allowed: string[]; // GitHub-permitted methods ([] = unknown → all three)
  preferred: string; // repo preference ("" = none)
  global_default: string; // global default ("" = never set)
  effective: string; // resolved method a merge would use
};

const ALL_STRATEGIES = ["squash", "merge", "rebase"];
const STRATEGY_LABEL: Record<string, string> = {
  squash: "Squash and merge",
  merge: "Create a merge commit",
  rebase: "Rebase and merge",
};

// MergeControl is the PR merge split-button: a primary action that runs the
// default execution mode (sandbox | host | plain, from Global settings) with the
// resolved merge strategy, plus a ▾ menu to switch mode and strategy per-merge.
// The strategy resolves repo → global → ask: the first time you merge a repo
// with no preference set anywhere, a small modal asks and remembers the choice
// for that repo. Only GitHub-allowed methods are ever offered.
//
// "host" opens the host-merge drawer (a live Claude session on a throwaway host
// checkout — fast, NOT sandboxed). "sandbox" launches a one-shot container that
// rebases + merges, then arms the poll-and-teardown watcher. "plain" is the
// classic direct `gh pr merge`.
function MergeControl({
  prId,
  repoId,
  pr,
  repoName,
}: {
  prId: number;
  repoId: string;
  pr: PrItem;
  repoName?: string;
}) {
  const [mode, setMode] = useState<MergeMode>("host");
  const [strat, setStrat] = useState<MergeStrategyState | null>(null);
  const [menuOpen, setMenuOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState<{ text: string; err: boolean } | null>(null);
  const [launchedProject, setLaunchedProject] = useState<string | null>(null);
  const [askOpen, setAskOpen] = useState(false); // first-time strategy modal
  // The strategy chosen in the ▾ menu / modal for THIS merge; falls back to the
  // resolved effective strategy.
  const [chosenStrat, setChosenStrat] = useState<string>("");

  // Load the global default mode + this repo's strategy state on mount.
  useEffect(() => {
    getJSON<{ merge_mode?: string }>("/global/config")
      .then((g) => {
        const m = g.merge_mode;
        if (m === "host" || m === "sandbox" || m === "plain") setMode(m);
      })
      .catch(() => {});
    getJSON<MergeStrategyState>(`/repos/${encodeURIComponent(repoId)}/merge-strategy`)
      .then((s) => {
        setStrat(s);
        setChosenStrat(s.effective || "");
      })
      .catch(() => {});
  }, [repoId]);

  // The methods to offer: GitHub-allowed, or all three when unknown.
  const options = strat && strat.allowed.length ? strat.allowed : ALL_STRATEGIES;
  // "Ask first" = no repo preference AND no global default set anywhere.
  const needsAsk = !!strat && !strat.preferred && !strat.global_default;

  // saveRepoStrategy persists a per-repo preference (from the modal or ▾ menu).
  const saveRepoStrategy = async (method: string) => {
    try {
      await postJSON(`/repos/${encodeURIComponent(repoId)}/merge-strategy`, { strategy: method });
      setStrat((s) => (s ? { ...s, preferred: method, effective: method } : s));
    } catch {
      /* best-effort; the merge still uses `method` for this run */
    }
  };

  // Kick off the primary action. If we still need to ask for a strategy, open the
  // modal first; the modal's Confirm calls runMerge with the chosen method.
  const start = () => {
    setMsg(null);
    if (needsAsk) {
      setAskOpen(true);
      return;
    }
    runMerge(chosenStrat || strat?.effective || "squash");
  };

  // runMerge executes the selected mode with the given strategy.
  const runMerge = async (method: string) => {
    setAskOpen(false);
    setMenuOpen(false);
    setBusy(true);
    setMsg(null);
    try {
      if (mode === "plain") {
        await postJSON(`/prs/${prId}/merge`, { method });
        setMsg({ text: "Merged ✓", err: false });
      } else if (mode === "host") {
        // Start a DETACHED host-merge job on the server, then open the Work tab
        // focused on it. The job keeps running if you navigate away — the Work
        // tab (in the Claude dock) lets you come back to it.
        await postJSON(`/prs/${prId}/merge-host/start`, {});
        window.dispatchEvent(new CustomEvent("corral:open-work"));
        setMsg({ text: "Merging on host — see the Work tab (⌘K)", err: false });
      } else {
        // sandbox: create a one-shot project on the PR branch, start it, submit
        // the merge prompt, then arm the poll-and-teardown watcher.
        const info = await getJSON<{ prompt: string; branch: string }>(`/prs/${prId}/merge-prompt`);
        const created = await postJSON<{ id: string }>("/projects/create", {
          mode: "clone",
          name: `${repoName || "pr"}-merge-${pr.number}`,
          repos: [{ repoId, branch: info.branch || pr.headRef || undefined }],
          source: { kind: "pr", repo_id: repoId, number: pr.number, url: pr.githubUrl, title: pr.title },
        });
        const id = created.id;
        setLaunchedProject(id);
        await postJSON(`/p/${id}/start`).catch(() => {});
        await postRaw(`/p/${id}/populate-prompt`, { prompt: info.prompt, submit: true }).catch(() => {});
        await postJSON(`/prs/${prId}/merge-watch`, { projectId: id }).catch(() => {});
        setMsg({ text: "Merge sandbox launched", err: false });
      }
    } catch (e) {
      setMsg({ text: (e as Error).message, err: true });
    } finally {
      setBusy(false);
    }
  };

  const notSandboxed = mode === "host";

  return (
    <span className="merge-control">
      <span className="split-btn">
        <button
          type="button"
          className={`btn primary split-main${notSandboxed ? " warn" : ""}`}
          disabled={busy}
          title={
            mode === "host"
              ? "Rebase & merge on the host (fast — NOT sandboxed)"
              : mode === "sandbox"
                ? "Rebase & merge in a one-shot sandbox, then tear it down"
                : "Merge the PR on GitHub (no rebase)"
          }
          onClick={start}
        >
          {busy ? "Working…" : `⑃ ${mergeModeLabel(mode)}`}
        </button>
        <button
          type="button"
          className="btn primary split-caret"
          disabled={busy}
          aria-label="Merge options"
          title="Choose merge mode & strategy"
          onClick={() => setMenuOpen((o) => !o)}
        >
          ▾
        </button>

        {menuOpen && (
          <div className="split-menu" role="menu">
            <div className="split-menu-head">Mode</div>
            {(["host", "sandbox", "plain"] as MergeMode[]).map((m) => (
              <button
                key={m}
                type="button"
                className={`split-menu-item${mode === m ? " active" : ""}`}
                onClick={() => setMode(m)}
              >
                {mergeModeLabel(m)}
                {m === "host" && <span className="split-menu-scope">not sandboxed</span>}
                {m === "plain" && <span className="split-menu-scope">no rebase</span>}
              </button>
            ))}
            <div className="split-menu-sep" />
            <div className="split-menu-head">
              Strategy{strat && strat.preferred ? " · repo default" : strat && strat.global_default ? " · global default" : ""}
            </div>
            {ALL_STRATEGIES.map((s) => {
              const allowedHere = options.includes(s);
              const active = (chosenStrat || strat?.effective) === s;
              return (
                <button
                  key={s}
                  type="button"
                  className={`split-menu-item${active ? " active" : ""}`}
                  disabled={!allowedHere}
                  title={allowedHere ? "" : "Disabled for this repo on GitHub"}
                  onClick={() => {
                    setChosenStrat(s);
                    saveRepoStrategy(s); // remember the choice for this repo
                  }}
                >
                  {STRATEGY_LABEL[s]}
                  {!allowedHere && <span className="split-menu-scope">off on GitHub</span>}
                </button>
              );
            })}
          </div>
        )}
      </span>

      {msg && <span className={`pr-actions-msg${msg.err ? " err" : ""}`}>{msg.text}</span>}
      {launchedProject && (
        <a className="pr-actions-link" href={`/p/${launchedProject}/`} title="Open the merge sandbox">
          open sandbox ↗
        </a>
      )}

      {/* First-time strategy modal: shown when nothing is set repo- or globally. */}
      {askOpen && (
        <div className="merge-ask-backdrop" onClick={() => setAskOpen(false)}>
          <div className="merge-ask" onClick={(e) => e.stopPropagation()}>
            <div className="merge-ask-head">How should {repoName || "this repo"} be merged?</div>
            <p className="muted">
              Pick the merge method for this repository. It'll be remembered as this repo's default (you can change
              it later in the ▾ menu). Only methods GitHub allows for the repo are shown.
            </p>
            <div className="merge-ask-options">
              {options.map((s) => (
                <button key={s} type="button" className="btn" onClick={() => runMerge(s)}>
                  {STRATEGY_LABEL[s]}
                </button>
              ))}
            </div>
            <button type="button" className="merge-ask-cancel" onClick={() => setAskOpen(false)}>
              Cancel
            </button>
          </div>
        </div>
      )}

    </span>
  );
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

// markdownBody renders a collapsible markdown body (PR or issue description).
// GitHub descriptions can be huge (templates, checklists), so clamp long ones.
function MarkdownBody({ text }: { text: string }) {
  const [open, setOpen] = useState(false);
  const long = text.length > 600;
  return (
    <div className={`pr-desc-body-wrap${long && !open ? " clamped" : ""}`}>
      <div className="pr-desc-body markdown" dangerouslySetInnerHTML={{ __html: renderMarkdown(text) }} />
      {long && (
        <button type="button" className="pr-desc-more" onClick={() => setOpen((v) => !v)}>
          {open ? "Show less" : "Show more"}
        </button>
      )}
    </div>
  );
}

// PRDescription shows the PR's description and, when the PR closes an issue (or
// links one via the head branch name), a tab per closing issue:
//
//   [ Description | Issue #42 | Issue #99 ]
//
// The linked issues are fetched lazily from /prs/<id>/issues. If there are none,
// it's just the PR description — no tab bar.
function PRDescription({ prId, body }: { prId: number; body?: string }) {
  const [issues, setIssues] = useState<LinkedIssue[]>([]);
  const [active, setActive] = useState(0); // 0 = PR; 1..n = issues[i-1]

  useEffect(() => {
    let live = true;
    getJSON<{ issues: LinkedIssue[] }>(`/prs/${prId}/issues`)
      .then((d) => live && setIssues(d.issues || []))
      .catch(() => live && setIssues([]));
    return () => {
      live = false;
    };
  }, [prId]);

  const prText = (body || "").trim();
  // Nothing to show at all (no PR body AND no issues): render nothing, as before.
  if (!prText && issues.length === 0) return null;

  // No linked issues → the original single-panel description (no tabs).
  if (issues.length === 0) {
    return (
      <div className="pr-desc">
        <div className="pr-desc-head">Description</div>
        {prText ? <MarkdownBody text={prText} /> : <p className="pr-desc-empty">No description.</p>}
      </div>
    );
  }

  const issue = active > 0 ? issues[active - 1] : null;
  return (
    <div className="pr-desc">
      <div className="pr-desc-tabs" role="tablist">
        <button
          type="button"
          role="tab"
          aria-selected={active === 0}
          className={`pr-desc-tab${active === 0 ? " active" : ""}`}
          onClick={() => setActive(0)}
        >
          Description
        </button>
        {issues.map((iss, i) => (
          <button
            key={iss.number}
            type="button"
            role="tab"
            aria-selected={active === i + 1}
            className={`pr-desc-tab${active === i + 1 ? " active" : ""}`}
            onClick={() => setActive(i + 1)}
            title={iss.title || `Issue #${iss.number}`}
          >
            Issue #{iss.number}
          </button>
        ))}
      </div>

      {issue ? (
        <div className="pr-desc-issue">
          <div className="pr-desc-issue-head">
            <span className="pr-desc-issue-title">
              #{issue.number} {issue.title || ""}
            </span>
            {issue.state && <span className="pr-state">{issue.state}</span>}
            {issue.url && (
              <a className="pr-desc-issue-link" href={issue.url} target="_blank" rel="noreferrer">
                view on GitHub ↗
              </a>
            )}
            {issue.source === "branch" && (
              <span className="pr-desc-issue-src" title="Linked via the PR's branch name, not an official 'Closes #N' reference">
                from branch name
              </span>
            )}
          </div>
          {(issue.body || "").trim() ? (
            <MarkdownBody text={(issue.body || "").trim()} />
          ) : (
            <p className="pr-desc-empty">No issue description.</p>
          )}
        </div>
      ) : prText ? (
        <MarkdownBody text={prText} />
      ) : (
        <p className="pr-desc-empty">No description.</p>
      )}
    </div>
  );
}

// PRNote is one local, private annotation on a PR (stored in Corral's DB, never
// posted to GitHub).
type PRNote = { id: number; prId: number; body: string; author?: string; createdAt: string };

// PRNotes is the private-notes panel: add/read/delete local notes on a PR. These
// are NEVER sent to GitHub — a scratchpad for you and your team. Mirrors the
// links panel's shape.
function PRNotes({ prId }: { prId: number }) {
  const [notes, setNotes] = useState<PRNote[] | null>(null);
  const [draft, setDraft] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  const load = () => {
    getJSON<{ notes: PRNote[] }>(`/prs/${prId}/notes`)
      .then((d) => setNotes(d.notes || []))
      .catch((e) => setErr((e as Error).message));
  };
  useEffect(load, [prId]);

  const add = () => {
    if (!draft.trim()) return;
    setBusy(true);
    setErr(null);
    postJSON(`/prs/${prId}/notes`, { body: draft })
      .then(() => {
        setDraft("");
        load();
      })
      .catch((e) => setErr((e as Error).message))
      .finally(() => setBusy(false));
  };

  const remove = (id: number) => {
    delJSON(`/prs/${prId}/notes/${id}`)
      .then(load)
      .catch((e) => setErr((e as Error).message));
  };

  return (
    <div className="pr-notes-panel">
      <div className="pr-notes-head">
        🗒 Notes <span className="pr-notes-sub">private · local only, never posted to GitHub</span>
      </div>
      <div className="pr-notes-add">
        <textarea
          className="pr-notes-input"
          rows={2}
          placeholder="Add a private note about this PR…"
          value={draft}
          onChange={(e) => setDraft(e.target.value)}
          onKeyDown={(e) => {
            // ⌘/Ctrl-Enter to save.
            if ((e.metaKey || e.ctrlKey) && e.key === "Enter") add();
          }}
        />
        <button type="button" className="btn primary" disabled={busy || !draft.trim()} onClick={add}>
          {busy ? "Saving…" : "Add note"}
        </button>
      </div>
      {err && <p className="tab-note err">{err}</p>}
      {notes === null ? (
        <p className="tab-note">Loading notes…</p>
      ) : notes.length === 0 ? (
        <p className="pr-notes-empty">No notes yet.</p>
      ) : (
        <ul className="pr-notes-list">
          {notes.map((n) => (
            <li key={n.id} className="pr-note">
              <div className="pr-note-body">{n.body}</div>
              <div className="pr-note-meta">
                {n.author ? `${n.author} · ` : ""}
                {n.createdAt}
                <button type="button" className="pr-note-del" title="Delete note" onClick={() => remove(n.id)}>
                  ✕
                </button>
              </div>
            </li>
          ))}
        </ul>
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
  const [notesOpen, setNotesOpen] = useState(false);
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
            <button
              type="button"
              className={`dock-toggle${notesOpen ? " on" : ""}`}
              title="Private notes on this PR — local only, never posted to GitHub"
              onClick={() => setNotesOpen((v) => !v)}
            >
              🗒 Notes
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

      <AgentContextStaleBanner repoId={repoId} />

      <div className="pr-review-page">
        <div className="pr-experimental-note">
          ⚠ <strong>Experimental — a gauge, not a substitute for review.</strong> This
          analysis is an aid to help you orient quickly; it can miss things and be
          wrong. Don’t rely on it in place of reading the diff and your own judgment.
        </div>
        {pr && linksOpen && (
          <div className="pr-links-panel">
            <LinkedPRs prId={pr.id} />
          </div>
        )}
        {pr && notesOpen && <PRNotes prId={pr.id} />}
        {pr && <PRActions prId={pr.id} repoId={repoId} pr={pr} repoName={repo?.name} />}
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
            <PRDescription prId={pr.id} body={pr.body} />
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
