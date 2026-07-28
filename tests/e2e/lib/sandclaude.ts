// Orchestration helpers for the sandclaude e2e suite. These wrap the sandclaude
// CLI, the inner DinD docker daemon, and the host-side log files so the specs
// can read as a narrative ("start, build inside DinD, hit :3000, check logs").
//
// The suite runs against a REAL sandbox: `sandclaude start` launches a detached,
// privileged outer container that runs an inner dockerd. The harness then drives
// the inner `docker build`/`docker run` itself (deterministic — no LLM in the
// loop) and asserts the full chain.

import { execFile, spawn } from 'node:child_process';
import { promisify } from 'node:util';
import { readFile } from 'node:fs/promises';
import { createHash } from 'node:crypto';
import * as path from 'node:path';

const execFileAsync = promisify(execFile);

/** Repo root, derived from this file's location (tests/e2e/lib/sandclaude.ts). */
export const REPO_ROOT = path.resolve(__dirname, '..', '..', '..');

/** The built host binary. install/build places it at the repo root. */
export const SANDCLAUDE_BIN = process.env.SANDCLAUDE_BIN || path.join(REPO_ROOT, 'sandclaude');

/** The isolated workspace the test inits sandclaude into (set by the spec). */
export const WORKSPACE = process.env.E2E_WORKSPACE || path.join(REPO_ROOT, 'tests', 'e2e', '.workspace');

// Port scheme for the DinD chain. The app listens on APP_PORT inside the inner
// container; the inner `docker run -p APP_PORT:APP_PORT` publishes it onto the
// DinD bridge gateway; a forwarder bridges the outer netns APP_PORT to that
// gateway; and the OUTER `docker run -p HOST_PORT:APP_PORT` exposes it to the
// host. HOST_PORT is deliberately NOT 3000 — dev machines (and this repo's own
// author) frequently run something on :3000, which would make the assertion
// hit the wrong server. 13000 is an unlikely collision.
export const APP_PORT = 3000;
export const HOST_PORT = Number(process.env.E2E_HOST_PORT || 13000);

export interface RunResult {
  code: number | null;
  stdout: string;
  stderr: string;
}

/** Run an arbitrary command, capturing output. Never throws on non-zero exit —
 *  callers assert on `code` so failures produce readable diffs, not stack traces. */
export function run(
  cmd: string,
  args: string[],
  opts: { cwd?: string; env?: NodeJS.ProcessEnv; timeoutMs?: number; input?: string } = {},
): Promise<RunResult> {
  return new Promise((resolve) => {
    const child = spawn(cmd, args, {
      cwd: opts.cwd || WORKSPACE,
      env: { ...process.env, ...opts.env },
    });
    let stdout = '';
    let stderr = '';
    child.stdout.on('data', (d) => (stdout += d.toString()));
    child.stderr.on('data', (d) => (stderr += d.toString()));
    if (opts.input) {
      child.stdin.write(opts.input);
      child.stdin.end();
    }
    const timer = opts.timeoutMs
      ? setTimeout(() => child.kill('SIGKILL'), opts.timeoutMs)
      : null;
    child.on('close', (code) => {
      if (timer) clearTimeout(timer);
      resolve({ code, stdout, stderr });
    });
  });
}

/** Run the sandclaude CLI from the test workspace. */
export function sandclaude(args: string[], opts: Parameters<typeof run>[2] = {}): Promise<RunResult> {
  return run(SANDCLAUDE_BIN, args, opts);
}

/** The outer container name sandclaude derives for a workspace: sandclaude_<basename>. */
export function outerContainerName(workspace = WORKSPACE): string {
  return 'sandclaude_' + path.basename(workspace);
}

/**
 * The named DinD data volume sandclaude derives for a workspace. MUST match Go's
 * config.DindVolumeName: "sandclaude-dind-" + first 6 bytes (12 hex chars) of
 * sha256(workspace). Used by teardown to remove ONLY the test's volume, never
 * another project's.
 */
export function dindVolumeName(workspace = WORKSPACE): string {
  const h = createHash('sha256').update(workspace).digest('hex').slice(0, 12);
  return 'sandclaude-dind-' + h;
}

/** Exec a command as root inside the outer sandbox container. */
export function execInOuter(args: string[], opts: { timeoutMs?: number } = {}): Promise<RunResult> {
  return run('docker', ['exec', '-u', 'root', outerContainerName(), ...args], {
    cwd: REPO_ROOT,
    timeoutMs: opts.timeoutMs,
  });
}

/** Exec a command against the INNER dockerd (the DinD daemon) via its socket. */
export function innerDocker(dockerArgs: string[], opts: { timeoutMs?: number } = {}): Promise<RunResult> {
  return execInOuter(
    ['docker', '--host', 'unix:///var/run/dind/docker.sock', ...dockerArgs],
    opts,
  );
}

/** Poll until `check` resolves truthy or the deadline passes. Returns the last value. */
export async function waitFor<T>(
  label: string,
  check: () => Promise<T | undefined>,
  { timeoutMs = 120_000, intervalMs = 2_000 }: { timeoutMs?: number; intervalMs?: number } = {},
): Promise<T> {
  const deadline = Date.now() + timeoutMs;
  let last: T | undefined;
  while (Date.now() < deadline) {
    last = await check();
    if (last) return last;
    await new Promise((r) => setTimeout(r, intervalMs));
  }
  throw new Error(`waitFor(${label}) timed out after ${timeoutMs}ms`);
}

/** True once the outer sandbox container is running. */
export async function outerRunning(): Promise<boolean> {
  const r = await run('docker', ['inspect', '-f', '{{.State.Running}}', outerContainerName()], {
    cwd: REPO_ROOT,
  });
  return r.code === 0 && r.stdout.trim() === 'true';
}

/** True once the inner dockerd answers `docker info`. */
export async function innerDockerdUp(): Promise<boolean> {
  const r = await innerDocker(['info'], { timeoutMs: 15_000 });
  return r.code === 0;
}

/** Read a host-side log file from <workspace>/.sandclaude/logs/. Empty string if absent. */
export async function readLog(name: string, workspace = WORKSPACE): Promise<string> {
  try {
    return await readFile(path.join(workspace, '.sandclaude', 'logs', name), 'utf8');
  } catch {
    return '';
  }
}

/** Read a log file from INSIDE the outer container's /home/claude/logs/. */
export async function readInnerLog(name: string): Promise<string> {
  const r = await execInOuter(['cat', `/home/claude/logs/${name}`]);
  return r.code === 0 ? r.stdout : '';
}

/**
 * GET a URL from the host and return {status, body}. Works identically on macOS
 * (Docker Desktop) and Linux CI because the assertion targets the HOST-published
 * port (outer `docker run -p 3000:3000`), which both platforms expose at
 * localhost:3000. The platform-specific piece (inner DinD -> outer interface) is
 * handled inside the container, not here.
 */
export async function hostGet(
  url: string,
  { timeoutMs = 5_000 }: { timeoutMs?: number } = {},
): Promise<{ status: number; body: string }> {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), timeoutMs);
  try {
    const res = await fetch(url, { signal: controller.signal });
    return { status: res.status, body: await res.text() };
  } finally {
    clearTimeout(timer);
  }
}

/**
 * Bridge, inside the outer container, its published port to the inner DinD
 * container's port on the DinD bridge gateway.
 *
 * Why this is needed: the outer `docker run -p <hostPort>:<outerPort>` publishes
 * the OUTER container's netns 0.0.0.0:<outerPort> to the host. The inner
 * `docker run -p <outerPort>:<appPort>` publishes onto the DinD bridge gateway
 * (172.18.0.1:<outerPort>), a DIFFERENT interface. Nothing connects the two, so
 * we run a tiny forwarder in the outer netns: 0.0.0.0:<outerPort> ->
 * 172.18.0.1:<outerPort>. Then host:<hostPort> -> outer:<outerPort> ->
 * gateway:<outerPort> -> inner app.
 *
 * Implemented with `node` (present in the sandbox image) rather than socat/nc —
 * those are NOT installed and the firewall blocks apt from fetching them. Runs
 * identically on macOS and Linux since it executes inside the Linux container.
 * Returns once the forwarder reports it is listening.
 */
export async function bridgePublishedPort(
  outerPort: number,
  gateway = '172.18.0.1',
): Promise<void> {
  const script =
    `const net=require('net');` +
    `const LP=${outerPort},TH='${gateway}',TP=${outerPort};` +
    `net.createServer(c=>{const u=net.connect(TP,TH);` +
    `c.pipe(u);u.pipe(c);const end=()=>{c.destroy();u.destroy();};` +
    `c.on('error',end);u.on('error',end);})` +
    `.listen(LP,'0.0.0.0',()=>console.log('fwd-up'));`;
  // Start detached inside the container.
  await run(
    'docker',
    ['exec', '-u', 'root', '-d', outerContainerName(), 'node', '-e', script],
    { cwd: REPO_ROOT },
  );
  // Give the listener a moment; the caller polls the HTTP endpoint anyway.
  await new Promise((r) => setTimeout(r, 1_000));
}
