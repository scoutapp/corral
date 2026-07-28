// Playwright global setup: bring up the real sandclaude sandbox once for the
// whole suite.
//
// Steps:
//   1. Build the host binary (go build -o <REPO_ROOT>/sandclaude ./cmd/sandclaude).
//   2. Create a clean, isolated WORKSPACE.
//   3. `sandclaude init` non-interactively: proxy ON, DinD ON, ports 3000:3000,
//      tmux OFF. We pipe the interactive answers AND then overwrite config.json
//      so the exact fields are guaranteed regardless of prompt drift.
//   4. `sandclaude start` (detached, browser suppressed).
//   5. Wait for the outer container to be Running and the inner dockerd to answer.
//
// WHY PROXY ON (firewall on), not --disable-firewall:
//   DinD's entrypoint installs a PREROUTING REDIRECT that sends every inner
//   container's external TCP to the transparent allowlist-proxy on :3129. That
//   redirect is applied whenever DinD is enabled, independent of the firewall
//   flag. With the firewall DISABLED nothing listens on :3129, so the fixture's
//   `npm install` (which needs registry.npmjs.org) would be redirected into a
//   dead port and fail. Proxy-on is therefore the only config in which the inner
//   build can reach the network. registry.npmjs.org is already in the default
//   allowlist, and the mitmproxy CA is auto-generated on proxy start and injected
//   into the build by the sandbox's bin/docker wrapper. Proxy-on needs NO real
//   Claude credentials to *start* (a dummy token is injected); the suite never
//   drives Claude, so that is fine.

import { execFile } from 'node:child_process';
import { promisify } from 'node:util';
import { mkdir, rm, writeFile, readFile } from 'node:fs/promises';
import * as path from 'node:path';
import {
  REPO_ROOT,
  SANDCLAUDE_BIN,
  WORKSPACE,
  APP_PORT,
  HOST_PORT,
  outerContainerName,
  run,
  sandclaude,
  waitFor,
  outerRunning,
  innerDockerdUp,
} from './lib/sandclaude';

const execFileAsync = promisify(execFile);

// Where we stash cross-spec artifacts (captured stdout, etc.) for both the
// "logs are correct" assertions and CI artifact upload.
export const ARTIFACTS_DIR = path.join(__dirname, '.artifacts');

function banner(msg: string) {
  // eslint-disable-next-line no-console
  console.log(`\n[e2e global-setup] ${msg}`);
}

async function stash(name: string, content: string) {
  await mkdir(ARTIFACTS_DIR, { recursive: true });
  await writeFile(path.join(ARTIFACTS_DIR, name), content, 'utf8');
}

export default async function globalSetup() {
  banner(`REPO_ROOT=${REPO_ROOT}`);
  banner(`WORKSPACE=${WORKSPACE}`);
  banner(`SANDCLAUDE_BIN=${SANDCLAUDE_BIN}`);

  await mkdir(ARTIFACTS_DIR, { recursive: true });

  // 1. Provide the host binary. When SANDCLAUDE_BIN is set (e.g. CI runs
  //    install.sh first and points here at /usr/local/bin/sandclaude), trust the
  //    caller's binary + its installed asset bundle and skip the build — this is
  //    how CI exercises the real install path. Otherwise build from source for a
  //    frictionless local `npm test`.
  if (process.env.SANDCLAUDE_BIN) {
    banner(`using pre-installed binary: ${SANDCLAUDE_BIN} (skipping go build)`);
  } else {
    banner('building sandclaude binary (go build)…');
    try {
      const { stdout, stderr } = await execFileAsync(
        'go',
        ['build', '-o', SANDCLAUDE_BIN, './cmd/sandclaude'],
        { cwd: REPO_ROOT, maxBuffer: 32 * 1024 * 1024 },
      );
      await stash('go-build.stdout.txt', stdout + '\n' + stderr);
    } catch (err: any) {
      const detail = `${err?.message ?? err}\nSTDOUT:\n${err?.stdout ?? ''}\nSTDERR:\n${err?.stderr ?? ''}`;
      await stash('go-build.error.txt', detail);
      throw new Error(`go build failed:\n${detail}`);
    }
  }

  // 2. Clean, isolated workspace. Remove any stale outer container/tmux first.
  banner('resetting workspace + any prior sandbox…');
  await bestEffortTeardown();
  await rm(WORKSPACE, { recursive: true, force: true });
  await mkdir(WORKSPACE, { recursive: true });

  // 3. init — piped answers, in prompt order:
  //    proxy? y | tmux? n | dind? y | ports -> HOST_PORT:APP_PORT | workspace -> blank (cwd)
  // run() defaults cwd to WORKSPACE, so the "current directory" workspace default
  // resolves to WORKSPACE.
  const portMapping = `${HOST_PORT}:${APP_PORT}`;
  banner(`sandclaude init (proxy on, dind on, ports ${portMapping}, tmux off)…`);
  const initInput = ['y', 'n', 'y', portMapping, ''].join('\n') + '\n';
  const init = await sandclaude(['init'], { cwd: WORKSPACE, input: initInput, timeoutMs: 120_000 });
  await stash('init.stdout.txt', init.stdout);
  await stash('init.stderr.txt', init.stderr);
  if (init.code !== 0) {
    throw new Error(
      `sandclaude init exited ${init.code}\nSTDOUT:\n${init.stdout}\nSTDERR:\n${init.stderr}`,
    );
  }

  // 3b. Deterministically overwrite config.json with the exact fields we want,
  //     independent of any prompt drift. Preserve created_at if init wrote it.
  const projectDir = path.join(WORKSPACE, '.sandclaude', 'project');
  const configPath = path.join(projectDir, 'config.json');
  let createdAt = new Date().toISOString();
  try {
    const existing = JSON.parse(await readFile(configPath, 'utf8'));
    if (existing.created_at) createdAt = existing.created_at;
  } catch {
    /* init should have written it; fall back to now */
  }
  const config = {
    workspace: WORKSPACE,
    proxy_enabled: true,
    dind_enabled: true,
    dind_ports: [portMapping],
    launch_tmux: false,
    created_at: createdAt,
  };
  await writeFile(configPath, JSON.stringify(config, null, 2), 'utf8');
  await stash('config.json', JSON.stringify(config, null, 2));
  banner(`wrote ${configPath}`);

  // 4. start — detached by default; suppress the browser tab. This also builds
  //    the sandclaude-stable image on first run (can take several minutes cold).
  banner('sandclaude start (detached, no browser)… this builds the image on a cold run');
  const start = await sandclaude(['start'], {
    cwd: WORKSPACE,
    env: { SANDCLAUDE_NO_BROWSER: '1' },
    timeoutMs: 900_000, // cold image build can be slow
  });
  await stash('start.stdout.txt', start.stdout);
  await stash('start.stderr.txt', start.stderr);
  if (start.code !== 0) {
    throw new Error(
      `sandclaude start exited ${start.code}\nSTDOUT:\n${start.stdout}\nSTDERR:\n${start.stderr}`,
    );
  }

  // 5. Wait for the outer container, then the inner dockerd.
  banner(`waiting for outer container ${outerContainerName()} to be Running…`);
  await waitFor('outerRunning', outerRunning, { timeoutMs: 180_000, intervalMs: 2_000 }).catch(
    async (e) => {
      await dumpDiagnostics();
      throw e;
    },
  );

  banner('waiting for inner dockerd to answer `docker info`…');
  await waitFor('innerDockerdUp', innerDockerdUp, { timeoutMs: 180_000, intervalMs: 3_000 }).catch(
    async (e) => {
      await dumpDiagnostics();
      throw e;
    },
  );

  banner('sandbox is up: outer container running, inner dockerd ready.');
}

// Capture everything useful for debugging a failed boot. The container is
// launched detached INSIDE a tmux session (startDetached), so a `docker run`
// that fails or exits immediately is invisible to `sandclaude start` (which
// returns 0 after creating the session). The tmux pane holds that output, and
// `docker ps -a` shows whether the container exists / exited — both essential
// on CI where we can't poke around interactively.
async function dumpDiagnostics() {
  const name = outerContainerName();
  const session = `sandclaude_${path.basename(WORKSPACE)}`;
  // What does docker see? (created / running / exited-with-code)
  await run('docker', ['ps', '-a', '--filter', `name=${name}`, '--format',
    'table {{.Names}}\t{{.Status}}\t{{.Ports}}'], { cwd: REPO_ROOT, timeoutMs: 15_000 })
    .then((r) => stash('docker-ps.txt', r.stdout + '\n' + r.stderr)).catch(() => {});
  // Container's own stdout/stderr (empty if it never started).
  await run('docker', ['logs', name], { cwd: REPO_ROOT, timeoutMs: 20_000 })
    .then((r) => stash('outer-container.logs.txt', r.stdout + '\n' + r.stderr)).catch(() => {});
  // The tmux pane where `docker run` actually ran — this is where a failed run
  // (bad flag, privileged denied, mount error) prints its error.
  await run('tmux', ['capture-pane', '-t', session, '-p', '-S', '-2000'],
    { cwd: REPO_ROOT, timeoutMs: 15_000 })
    .then((r) => stash('tmux-pane.txt', r.stdout + '\n' + r.stderr)).catch(() => {});
}

// Kill a lingering outer container + tmux session from a prior run so a fresh
// start isn't blocked by a name clash.
async function bestEffortTeardown() {
  const name = outerContainerName();
  await run('docker', ['rm', '-f', name], { cwd: REPO_ROOT, timeoutMs: 30_000 }).catch(() => {});
  await run('tmux', ['kill-session', '-t', `sandclaude_${path.basename(WORKSPACE)}`], {
    cwd: REPO_ROOT,
    timeoutMs: 10_000,
  }).catch(() => {});
}
