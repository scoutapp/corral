// Playwright global teardown: best-effort cleanup of the shared sandbox.
//
// We intentionally do NOT call `sandclaude remove` (it prompts, and it deletes
// the workspace's .sandclaude/logs that CI uploads as artifacts). Instead we:
//   - force-remove the outer container (its EXIT trap stops the inner dockerd +
//     inner containers),
//   - kill the host tmux session that owns the detached container,
//   - stop the dashboard daemon.
// Host-side logs under <workspace>/.sandclaude/logs and tests/e2e/.artifacts are
// left intact for artifact upload. This function never throws.

import {
  REPO_ROOT,
  WORKSPACE,
  SANDCLAUDE_BIN,
  outerContainerName,
  dindVolumeName,
  run,
} from './lib/sandclaude';

function log(msg: string) {
  // eslint-disable-next-line no-console
  console.log(`[e2e global-teardown] ${msg}`);
}

export default async function globalTeardown() {
  const name = outerContainerName();
  const session = name; // tmux session is derived from the (tmux-safe) container name

  // Stop any inner containers gracefully first (best-effort), then nuke the outer.
  await run(
    'docker',
    ['exec', '-u', 'root', name, 'sh', '-c',
      'docker --host unix:///var/run/dind/docker.sock ps -q | xargs -r docker --host unix:///var/run/dind/docker.sock rm -f'],
    { cwd: REPO_ROOT, timeoutMs: 30_000 },
  ).catch(() => {});

  log(`removing outer container ${name}…`);
  await run('docker', ['rm', '-f', name], { cwd: REPO_ROOT, timeoutMs: 60_000 }).catch(() => {});

  log(`killing tmux session ${session}…`);
  await run('tmux', ['kill-session', '-t', session], { cwd: REPO_ROOT, timeoutMs: 10_000 }).catch(
    () => {},
  );

  log('stopping dashboard daemon…');
  await run(SANDCLAUDE_BIN, ['dashboard', 'stop'], { cwd: WORKSPACE, timeoutMs: 15_000 }).catch(
    () => {},
  );

  // Remove ONLY this test workspace's DinD data volume (named deterministically
  // from the workspace path) so repeated local runs don't accumulate volumes.
  // Other projects' sandclaude-dind-* volumes are never touched.
  const vol = dindVolumeName();
  log(`removing test DinD volume ${vol}…`);
  await run('docker', ['volume', 'rm', '-f', vol], { cwd: REPO_ROOT, timeoutMs: 15_000 }).catch(
    () => {},
  );

  log('done (logs left intact for artifact upload).');
}
