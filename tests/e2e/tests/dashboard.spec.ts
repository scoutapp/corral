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
  // Auth cookie is set by the previous navigation within the same context? No —
  // each test gets a fresh context. Prime the cookie by hitting the token URL,
  // then fetch the static asset via the page's request context (carries cookies).
  await page.goto(`${baseURL}/?token=${token}`);
  const css = await page.request.get(`${baseURL}/static/dashboard.css`);
  expect(css.status()).toBe(200);
  expect(css.headers()['content-type'] || '').toContain('css');
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

  // Open the project's own page and assert it renders with the workspace in the
  // title and the project id on <body data-project-id>.
  projectHref = `${baseURL}/p/${mine!.id}`;
  const projResp = await page.goto(`${projectHref}?token=${token}`);
  expect(projResp?.status()).toBe(200);
  await expect(page).toHaveTitle(new RegExp(`${escapeRegExp(PROJECT_NAME)}.*sandclaude`));
  await expect(page.locator('body')).toHaveAttribute('data-project-id', mine!.id);

  // The tab bar is server-rendered; assert the Config + Mitm tabs exist.
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

test.skip('mitm flows render', async () => {
  // SOFTENED/SKIPPED: proxy IS enabled in this suite, but the mitm flow table is
  // only populated once intercepted HTTPS traffic has flowed through mitmweb.
  // The suite doesn't drive Claude, so there may be no flows to assert on. Left
  // as a documented placeholder rather than a flaky assertion. To exercise it,
  // generate allowed traffic from inside the container and poll /p/<id>/mitm/flows.
});

function escapeRegExp(s: string): string {
  return s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
}

void projectHref;
