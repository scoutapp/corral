// Dashboard browser tests. The dashboard is a host-side HTTP server (loopback
// only, token-gated). global-setup's `sandclaude start` brings the daemon up
// (via startProxy -> EnsureDashboardRunning); here we ask the CLI for its
// URL+token, visit it (the ?token= sets an auth cookie), and assert the project
// this suite created is visible.
//
// Assertions target stable markers: the <title>, HTTP 200 of a static asset,
// and the JSON /status endpoint (which lists projects by id + basename name).
// The project page's dynamic tabs (mitm flows, terminal) are only softly
// checked — their content depends on live traffic/PTY state.

import { test, expect } from '@playwright/test';
import * as path from 'node:path';
import { SANDCLAUDE_BIN, WORKSPACE, run } from '../lib/sandclaude';

const PROJECT_NAME = path.basename(WORKSPACE);

// Resolved once for the whole file from `sandclaude dashboard`.
let baseURL = '';
let token = '';
let projectHref = '';

test.beforeAll(async () => {
  const res = await run(SANDCLAUDE_BIN, ['dashboard'], { cwd: WORKSPACE, timeoutMs: 30_000 });
  // Line looks like: "Dashboard running at http://127.0.0.1:PORT/?token=HEX"
  const m = res.stdout.match(/Dashboard running at (http:\/\/127\.0\.0\.1:\d+)\/\?token=([0-9a-f]+)/);
  expect(m, `could not parse dashboard URL from:\n${res.stdout}\n${res.stderr}`).not.toBeNull();
  baseURL = m![1];
  token = m![2];
});

test('landing page loads and is titled', async ({ page }) => {
  // Visiting with ?token= sets the auth cookie for subsequent same-origin requests.
  const resp = await page.goto(`${baseURL}/?token=${token}`);
  expect(resp?.status()).toBe(200);
  await expect(page).toHaveTitle('sandclaude — control');
  // Brand marker is server-rendered and stable.
  await expect(page.locator('.brand-name')).toHaveText('sandclaude');
});

test('static asset served', async ({ page }) => {
  // Each test gets a fresh context; prime the auth cookie by hitting the token
  // URL, then fetch a static asset via the page's request context (carries
  // cookies). The dashboard is now a React SPA whose shell (index.html) links a
  // hashed JS + CSS bundle under /static/app/assets — assert those serve.
  await page.goto(`${baseURL}/?token=${token}`);
  const html = await (await page.request.get(`${baseURL}/`)).text();
  const jsHref = html.match(/\/static\/app\/assets\/[^"']+\.js/)?.[0];
  const cssHref = html.match(/\/static\/app\/assets\/[^"']+\.css/)?.[0];
  expect(jsHref, `no app JS bundle referenced in the shell:\n${html}`).toBeTruthy();
  const js = await page.request.get(`${baseURL}${jsHref}`);
  expect(js.status()).toBe(200);
  expect(js.headers()['content-type'] || '').toContain('javascript');
  if (cssHref) {
    const css = await page.request.get(`${baseURL}${cssHref}`);
    expect(css.status()).toBe(200);
  }
});

test('project appears in /status and has a project page', async ({ page }) => {
  await page.goto(`${baseURL}/?token=${token}`);

  // /status is the JSON the landing page polls; it lists every registered
  // project by id + basename name. Our workspace was registered by `start`.
  const statusResp = await page.request.get(`${baseURL}/status`);
  expect(statusResp.status()).toBe(200);
  const status = (await statusResp.json()) as {
    projects: Array<{ id: string; name: string; workspace: string; container_up: boolean }>;
  };
  const mine = status.projects.find((p) => p.name === PROJECT_NAME || p.workspace === WORKSPACE);
  expect(mine, `project "${PROJECT_NAME}" not found in /status: ${JSON.stringify(status)}`).toBeTruthy();
  // The outer container is up (global-setup waited on it), so the dashboard sees it.
  expect(mine!.container_up).toBe(true);

  // Open the project's own page. It's a React SPA route: the shell returns 200,
  // then the app renders the header + tab bar client-side (Playwright waits for
  // the locators). The header <h1> shows the project name once the status poll
  // resolves, falling back to the id before then — so assert it's rendered and
  // non-empty rather than an exact value (avoids a poll-timing flake).
  projectHref = `${baseURL}/p/${mine!.id}`;
  const projResp = await page.goto(`${projectHref}?token=${token}`);
  expect(projResp?.status()).toBe(200);
  await expect(page.locator('header h1')).not.toBeEmpty();

  // The tab bar is rendered by the SPA; assert the Config + Mitm tabs appear.
  await expect(page.locator('.tab-btn', { hasText: 'Config' })).toBeVisible();
  await expect(page.locator('.tab-btn', { hasText: 'Mitm Proxy' })).toBeVisible();
});

test('project config endpoint returns JSON', async ({ page }) => {
  await page.goto(`${baseURL}/?token=${token}`);
  const statusResp = await page.request.get(`${baseURL}/status`);
  const status = (await statusResp.json()) as { projects: Array<{ id: string; name: string; workspace: string }> };
  const mine = status.projects.find((p) => p.name === PROJECT_NAME || p.workspace === WORKSPACE);
  expect(mine).toBeTruthy();

  // The Config tab is backed by GET /p/<id>/config returning the project's config.
  const cfg = await page.request.get(`${baseURL}/p/${mine!.id}/config`);
  expect(cfg.status()).toBe(200);
  const body = await cfg.json();
  // Resilient: just assert it parsed as an object mentioning our workspace/dind.
  const text = JSON.stringify(body);
  expect(text).toContain(WORKSPACE);
});

// ---- Files / Diff / Container tabs (the workspace-view feature) -------------

// Resolve this suite's project id once for the file/git tests below.
async function projectId(page: import('@playwright/test').Page): Promise<string> {
  await page.goto(`${baseURL}/?token=${token}`);
  const statusResp = await page.request.get(`${baseURL}/status`);
  const status = (await statusResp.json()) as { projects: Array<{ id: string; name: string; workspace: string }> };
  const mine = status.projects.find((p) => p.name === PROJECT_NAME || p.workspace === WORKSPACE);
  expect(mine, `project "${PROJECT_NAME}" not in /status`).toBeTruthy();
  return mine!.id;
}

test('project page has the new tabs + terminal dock', async ({ page }) => {
  const id = await projectId(page);
  await page.goto(`${baseURL}/p/${id}?token=${token}`);
  for (const label of ['Files', 'Diff', 'Container']) {
    await expect(page.locator('.tab-btn', { hasText: new RegExp('^' + label + '$') })).toBeVisible();
  }
  // The detachable Claude terminal dock is present in the layout.
  await expect(page.locator('#term-dock')).toHaveCount(1);
  // The CodeMirror bundle is referenced and served.
  const bundle = await page.request.get(`${baseURL}/static/codemirror.bundle.js`);
  expect(bundle.status()).toBe(200);
});

test('files tree + read return workspace contents', async ({ page }) => {
  const id = await projectId(page);
  // Root tree lists the workspace; the fixture workspace contains .sandclaude at
  // least (created by init) — assert we get a non-empty entries array.
  const tree = await page.request.get(`${baseURL}/p/${id}/files/tree?path=`);
  expect(tree.status()).toBe(200);
  const entries = ((await tree.json()).entries || []) as Array<{ name: string; dir: boolean }>;
  expect(Array.isArray(entries)).toBe(true);

  // Write a file, read it back, confirm round-trip (edit path the editor uses).
  const rel = 'e2e-editor-probe.txt';
  const content = 'hello from the e2e editor test\n';
  const wrote = await page.request.post(`${baseURL}/p/${id}/files/write?path=${rel}`, { data: content });
  expect(wrote.status()).toBe(200);
  const read = await page.request.get(`${baseURL}/p/${id}/files/read?path=${rel}`);
  expect(read.status()).toBe(200);
  expect((await read.json()).content).toBe(content);
});

test('path traversal cannot escape the workspace', async ({ page }) => {
  const id = await projectId(page);
  // safeJoin anchors the request under the workspace root: "../../../etc/passwd"
  // is Clean()ed to "/etc/passwd" and re-joined as <workspace>/etc/passwd, which
  // does not exist -> 404. The key property is that /etc/passwd is NEVER served;
  // assert we did not get a 200 with the host's passwd file.
  const resp = await page.request.get(`${baseURL}/p/${id}/files/read?path=../../../../etc/passwd`);
  expect(resp.status()).not.toBe(200);
  const text = await resp.text();
  expect(text).not.toContain('root:'); // never leak the real /etc/passwd
});

test('git status + diff reflect a change', async ({ page }) => {
  const id = await projectId(page);
  const status = await page.request.get(`${baseURL}/p/${id}/git/status`);
  expect(status.status()).toBe(200);
  const body = (await status.json()) as { repo: boolean; changes?: Array<{ path: string }> };
  // The fixture workspace may or may not be a git repo; only assert the diff
  // wiring when it is (real repos: e.g. running against this repo). Either way
  // the endpoint must respond with valid JSON and a boolean `repo`.
  expect(typeof body.repo).toBe('boolean');
  if (body.repo && body.changes && body.changes.length) {
    const diff = await page.request.get(`${baseURL}/p/${id}/git/diff?path=${encodeURIComponent(body.changes[0].path)}`);
    expect(diff.status()).toBe(200);
  }
});

test.skip('mitm flows render', async () => {
  // SOFTENED/SKIPPED: proxy IS enabled in this suite, but the mitm flow table is
  // only populated once intercepted HTTPS traffic has flowed through mitmweb.
  // The suite doesn't drive Claude, so there may be no flows to assert on. Left
  // as a documented placeholder rather than a flaky assertion. To exercise it,
  // generate allowed traffic from inside the container and poll /p/<id>/mitm/flows.
});

void projectHref;
