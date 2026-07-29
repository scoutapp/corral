// The core end-to-end chain, driven purely through the orchestration harness
// (no browser). Serialized so build -> run -> assert happen in order against the
// single shared sandbox that global-setup brought up.
//
//   host:3000  ──▶  outer sandbox container (socat bridge)  ──▶  inner DinD
//                                                                container :3000
//
// The inner image is built through the sandbox's `bin/docker` wrapper (NOT the
// raw docker binary): the wrapper injects the mitmproxy CA into the build so the
// fixture's `npm install` over HTTPS-through-the-allowlist-proxy is trusted. The
// harness's innerDocker() helper calls /usr/bin/docker directly and would bypass
// that injection, so the build step here invokes the wrapper explicitly.

import { test, expect } from '@playwright/test';
import * as path from 'node:path';
import {
  REPO_ROOT,
  APP_PORT,
  HOST_PORT,
  outerContainerName,
  execInOuter,
  innerDocker,
  run,
  waitFor,
  hostGet,
  readInnerLog,
  bridgePublishedPort,
} from '../lib/sandclaude';

const FIXTURE_DIR = path.join(__dirname, '..', 'fixtures', 'web-app');
const IMAGE = 'sandclaude-e2e-web';
const CONTAINER = 'web';
const INNER_DOCKER = 'unix:///var/run/dind/docker.sock';
const ARTIFACTS_DIR = path.join(__dirname, '..', '.artifacts');

test.describe.serial('sandclaude DinD chain', () => {
  test('inner image builds inside DinD', async () => {
    // Copy the fixture into the outer container's filesystem so the inner
    // `docker build` (running inside the same outer container) can use it as a
    // build context.
    const dest = '/home/claude/e2e-web-app';
    const name = outerContainerName();

    // Fresh copy each run.
    await execInOuter(['rm', '-rf', dest], { timeoutMs: 30_000 });
    const cp = await run('docker', ['cp', `${FIXTURE_DIR}/.`, `${name}:${dest}`], {
      cwd: REPO_ROOT,
      timeoutMs: 60_000,
    });
    expect(cp.code, `docker cp fixture -> container\n${cp.stderr}`).toBe(0);

    // Build via the CA-injecting wrapper. DOCKER_HOST points the wrapper (and the
    // real docker it exec's) at the inner dockerd.
    //
    // Proxy build-args: inside a DinD build container the shell's HTTP(S)_PROXY
    // (host mitmweb, e.g. 192.168.65.254 on Docker Desktop) is unreachable. The
    // reachable proxy is the allowlist proxy on the DinD bridge gateway,
    // 172.18.0.1:3128. sandclaude's own `startDocker` passes exactly these
    // build-args (see internal/container/docker.go); a build driven manually
    // (as here, and as a user would) must pass them too. NO_PROXY covers the
    // inner bridge + loopback so intra-DinD/registry-metadata calls stay direct.
    // Two proxy concerns, both pointed at the reachable gateway 172.18.0.1:3128
    // (the allowlist proxy on the DinD bridge), NOT the outer container's inherited
    // HTTP_PROXY (host mitmweb at host.docker.internal:9506, unreachable from the
    // DinD build):
    //   1. BuildKit's own image-metadata / registry-token fetch reads HTTP(S)_PROXY
    //      from the build client's ENV. We override it in the exec env so the
    //      `FROM node:20-slim` manifest pull succeeds.
    //   2. The Dockerfile's RUN steps (npm install) read the PROXY *build-args*.
    // sandclaude's own startDocker passes the same values (internal/container/docker.go).
    const proxyEnv =
      `HTTP_PROXY=http://172.18.0.1:3128 HTTPS_PROXY=http://172.18.0.1:3128 ` +
      `NO_PROXY=172.18.0.0/16,127.0.0.0/8,localhost`;
    const buildCmd =
      `export DOCKER_HOST=${INNER_DOCKER} ${proxyEnv}; ` +
      `/home/claude/bin/docker build ` +
      `--build-arg HTTP_PROXY=http://172.18.0.1:3128 ` +
      `--build-arg HTTPS_PROXY=http://172.18.0.1:3128 ` +
      `--build-arg NO_PROXY=172.18.0.0/16,127.0.0.0/8,localhost ` +
      `-t ${IMAGE} ${dest}`;

    // Retry the build a few times. The base-image manifest pull
    // (`FROM node:20-slim` → HEAD registry-1.docker.io) goes through the mitm
    // proxy, and there is a startup race: the entrypoint installs the mitm CA
    // into the inner dockerd's trust store shortly AFTER dockerd is up, so a
    // build fired immediately can hit "x509: certificate signed by unknown
    // authority" (or a transient registry error). A short bounded retry rides
    // over that window without masking a real, persistent failure.
    let build = { code: 1, stdout: '', stderr: '' } as { code: number | null; stdout: string; stderr: string };
    for (let attempt = 1; attempt <= 4; attempt++) {
      build = await execInOuter(['sh', '-c', buildCmd], { timeoutMs: 600_000 });
      await writeArtifact(`inner-build.attempt${attempt}.log`, `${build.stdout}\n---STDERR---\n${build.stderr}`);
      if (build.code === 0) break;
      const transient = /certificate signed by unknown authority|failed to (do request|resolve source metadata)|TLS handshake|i\/o timeout/i.test(build.stderr);
      if (!transient) break; // a real build error — fail fast, don't spin
      await new Promise((r) => setTimeout(r, 5000));
    }

    expect(build.code, `inner docker build failed after retries\nSTDOUT:\n${build.stdout}\nSTDERR:\n${build.stderr}`).toBe(0);

    // Confirm the image now exists in the inner daemon.
    const images = await innerDocker(['images', '--format', '{{.Repository}}'], { timeoutMs: 30_000 });
    expect(images.code).toBe(0);
    expect(images.stdout).toContain(IMAGE);
  });

  test('host reaches inner app through the full DinD port chain', async () => {
    // Remove any prior instance, then run the freshly-built image detached with
    // its port published on the inner DinD bridge gateway (APP_PORT:APP_PORT).
    await innerDocker(['rm', '-f', CONTAINER], { timeoutMs: 30_000 });
    const runInner = await innerDocker(
      ['run', '-d', '-p', `${APP_PORT}:${APP_PORT}`, '--name', CONTAINER, IMAGE],
      { timeoutMs: 60_000 },
    );
    expect(runInner.code, `inner docker run failed\n${runInner.stderr}`).toBe(0);

    // Bridge outer-netns APP_PORT -> DinD gateway APP_PORT. The outer container
    // publishes HOST_PORT:APP_PORT to the host (start config), completing:
    // host:HOST_PORT -> outer:APP_PORT -> gateway:APP_PORT -> inner app.
    await bridgePublishedPort(APP_PORT);

    const base = `http://localhost:${HOST_PORT}`;
    // Root route returns the exact marker the fixture serves.
    const rootRes = await waitFor(
      `GET ${base}/ == 200 with marker`,
      async () => {
        try {
          const r = await hostGet(`${base}/`, { timeoutMs: 4_000 });
          return r.status === 200 && r.body.includes('sandclaude e2e ok') ? r : undefined;
        } catch {
          return undefined;
        }
      },
      { timeoutMs: 60_000, intervalMs: 2_000 },
    );
    expect(rootRes.status).toBe(200);
    expect(rootRes.body).toContain('sandclaude e2e ok');

    // Health route returns JSON {status:"ok"}.
    const health = await hostGet(`${base}/healthz`, { timeoutMs: 5_000 });
    expect(health.status).toBe(200);
    expect(health.body).toContain('"status":"ok"');
  });

  test('logs and boot evidence are correct', async () => {
    // The captured `sandclaude start` output was stashed by global-setup. The
    // operational lines (Go's log package) go to STDERR while the tmux banner
    // goes to stdout, so assert against both streams combined.
    const startOut =
      (await readArtifact('start.stdout.txt')) + '\n' + (await readArtifact('start.stderr.txt'));
    expect(startOut.trim(), 'start output was captured by global-setup').not.toBe('');
    expect(startOut).toMatch(/DinD enabled/);
    expect(startOut).toContain(`${HOST_PORT}:${APP_PORT}`);

    // With DinD on, the entrypoint writes dockerd.log inside the container. The
    // inner dockerd is already proven up (global-setup waited on `docker info`),
    // and cert-injector.log exists because proxy mode places /etc/proxy-ca.crt
    // and the entrypoint starts the injector.
    const certLog = await readInnerLog('cert-injector.log');
    expect(certLog, 'cert-injector.log should exist (proxy mode + DinD)').not.toBe('');

    const dockerdLog = await readInnerLog('dockerd.log');
    expect(dockerdLog, 'dockerd.log should exist (DinD enabled)').not.toBe('');

    // Proxy mode also produces proxy.log inside the container (the allowlist proxy)
    // and mitm.log on the host. proxy.log is the authoritative in-container one.
    const proxyLog = await readInnerLog('proxy.log');
    expect(proxyLog, 'proxy.log should exist (firewall/proxy enabled)').not.toBe('');
  });
});

async function readArtifact(name: string): Promise<string> {
  const { readFile } = await import('node:fs/promises');
  try {
    return await readFile(path.join(ARTIFACTS_DIR, name), 'utf8');
  } catch {
    return '';
  }
}

async function writeArtifact(name: string, content: string): Promise<void> {
  const { mkdir, writeFile } = await import('node:fs/promises');
  try {
    await mkdir(ARTIFACTS_DIR, { recursive: true });
    await writeFile(path.join(ARTIFACTS_DIR, name), content, 'utf8');
  } catch {
    /* best-effort */
  }
}
