import { expect, test } from '@playwright/test';

import type { StackDetail } from '../src/api/types';
import { clone, fullPlanText, graph, ids, stack, truncatedPlanStack } from '../src/fixtures';
import { mockApi } from './mock-api';

test.describe('signed in', () => {
  test('overview shows counts, statuses and recent runs', async ({ page }) => {
    await mockApi(page);
    await page.goto('/');
    await expect(page.getByRole('heading', { level: 1, name: 'Overview' })).toBeVisible();
    await expect(page.locator('.stat').filter({ hasText: 'Locks held' })).toContainText('5');
    await expect(page.locator('.stat').filter({ hasText: 'Drifted' })).toContainText('2');
    await expect(page.getByRole('region', { name: 'Stacks by status' })).toContainText('failed1');
    const recent = page.getByRole('table', { name: 'Recent runs' });
    await expect(recent.getByRole('row')).toHaveCount(6);
    await expect(recent).toContainText('apply · PR #42');
    await expect(page.locator('.user-menu')).toContainText('octocat');
    await expect(page).toHaveTitle('Overview · Stackorder');
  });

  test('navigates from the repositories list to the graph and into a stack by clicking a node', async ({ page }) => {
    await mockApi(page);
    await page.goto('/');
    await page.getByRole('navigation', { name: 'Main' }).getByRole('link', { name: 'Repositories' }).click();
    await expect(page).toHaveURL('/repos');
    await page.getByRole('link', { name: 'acme/infra' }).click();
    await expect(page).toHaveURL('/repos/acme/infra');

    const graph = page.getByRole('group', { name: 'Dependency graph of acme/infra' });
    await expect(graph.locator('.node')).toHaveCount(8);
    await expect(graph.locator('.node--module')).toHaveCount(3);
    await expect(graph.locator('path.edge--reads_state.edge--inferred')).toHaveCount(1);
    await expect(graph.locator('path.edge--uses_module')).toHaveCount(5);
    await expect(page.getByRole('heading', { name: 'Legend' })).toBeVisible();

    await graph.getByRole('link', { name: 'Stack stacks/prod/eks' }).click();
    await expect(page).toHaveURL(`/stacks/${ids.stack}`);
    await expect(page.getByRole('heading', { level: 1, name: 'stacks/prod/eks' })).toBeVisible();
  });

  test('graph nodes are reachable with the keyboard', async ({ page }) => {
    await mockApi(page);
    await page.goto('/repos/acme/infra');
    const vpc = page.getByRole('link', { name: 'Stack stacks/prod/vpc' });
    await vpc.focus();
    await expect(vpc).toBeFocused();
    await page.keyboard.press('Enter');
    await expect(page).toHaveURL(`/stacks/${ids.stacks['stacks/prod/vpc'] ?? ''}`);
  });

  test('replays a run on the graph from the run selector', async ({ page }) => {
    const calls = await mockApi(page);
    await page.goto('/repos/acme/infra');
    await expect(page.getByRole('group', { name: 'Dependency graph of acme/infra' })).toBeVisible();
    await page.getByLabel('Replay run').selectOption(ids.run);
    await expect(page).toHaveURL(`/repos/acme/infra?run=${ids.run}`);

    const panel = page.getByRole('region', { name: 'Affected set' });
    await expect(panel).toContainText('5 affected stacks in 3 waves.');
    await expect(panel.getByRole('heading', { level: 3 })).toHaveText(['Wave 0', 'Wave 1', 'Wave 2']);
    const graph = page.getByRole('group', { name: 'Dependency graph of acme/infra' });
    await expect(graph.locator('.node__wave text')).toHaveText(['W0', 'W0', 'W1', 'W1', 'W2']);
    await expect(graph.locator('.node[data-key="stacks/prod/eks"]')).toHaveClass(/node--tone-danger/);
    await expect(graph.locator('.node[data-key="stacks/prod/apps"]')).toHaveClass(/node--tone-warning/);
    await expect(graph.locator('.node[data-key="acme/infra//modules/eks"]')).toHaveClass(/node--dimmed/);
    expect(calls.some((c) => c.path === '/v1/repos/acme/infra/graph' && c.search === `?run=${ids.run}`)).toBe(true);
  });

  test('reads the graph at a commit prefix from the ref box and explains refused refs', async ({ page }) => {
    const reason = '"main" is not a commit SHA, a SHA prefix of at least 7 characters or "default"; branch names are not supported';
    const calls = await mockApi(page, (c) =>
      c.path === '/v1/repos/acme/infra/graph' && c.search === '?ref=main'
        ? { status: 400, body: { code: 'invalid', message: `invalid ref: ${reason}`, details: { field: 'ref', reason } } }
        : undefined,
    );
    await page.goto('/repos/acme/infra');
    const ref = page.getByLabel('Ref');
    await expect(ref).toHaveAttribute('placeholder', 'commit SHA or default');
    await expect(ref).toHaveAccessibleDescription(
      'A full commit SHA, a unique prefix of at least 7 characters, or default for the default branch. Branch names are not supported; leave it empty for the current graph.',
    );
    await ref.fill('3f2a9c1');
    await page.getByRole('button', { name: 'Show' }).click();
    await expect(page).toHaveURL('/repos/acme/infra?ref=3f2a9c1');
    await expect(page.getByRole('group', { name: 'Dependency graph of acme/infra' })).toBeVisible();
    expect(calls.some((c) => c.path === '/v1/repos/acme/infra/graph' && c.search === '?ref=3f2a9c1')).toBe(true);

    await ref.fill('main');
    await page.getByRole('button', { name: 'Show' }).click();
    await expect(page).toHaveURL('/repos/acme/infra?ref=main');
    await expect(page.getByRole('alert')).toContainText(`invalid ref: ${reason}`);
  });

  test('draws inferred edges dashed and explicit edges solid', async ({ page }) => {
    const view = clone(graph);
    const edges = view.graph.edges ?? [];
    const promoted = clone(edges.find((e) => e.type === 'reads_state' && e.inferred));
    if (!promoted) throw new Error('fixture graph has no inferred reads_state edge');
    promoted.inferred = false;
    promoted.from = { kind: 'stack', key: 'stacks/staging/eks' };
    promoted.to = { kind: 'stack', key: 'stacks/prod/vpc' };
    view.graph.edges = [...edges, promoted];
    await mockApi(page, (c) => (c.path === '/v1/repos/acme/infra/graph' ? { body: view } : undefined));
    await page.goto('/repos/acme/infra');
    const svg = page.getByRole('group', { name: 'Dependency graph of acme/infra' });
    const dash = (selector: string) => svg.locator(selector).evaluate((el) => getComputedStyle(el).strokeDasharray);
    await expect(svg.locator('path.edge--reads_state')).toHaveCount(2);
    expect(await dash('path.edge--reads_state.edge--inferred')).not.toBe('none');
    expect(await dash('path.edge--reads_state:not(.edge--inferred)')).toBe('none');
    expect(await dash('path.edge--depends_on >> nth=0')).toBe('none');
  });

  test('pans with a drag and zooms with the wheel', async ({ page }) => {
    await mockApi(page);
    await page.goto('/repos/acme/infra');
    const svg = page.getByRole('group', { name: 'Dependency graph of acme/infra' });
    const viewport = svg.locator('.graph__viewport');
    await expect(viewport).toHaveAttribute('transform', 'translate(0,0) scale(1)');

    const box = await svg.boundingBox();
    if (!box) throw new Error('graph has no box');
    await page.mouse.move(box.x + 8, box.y + 8);
    await page.mouse.wheel(0, -300);
    await expect(page.locator('.graph__zoom')).not.toHaveText('100%');

    await page.getByRole('button', { name: 'Reset view' }).click();
    await expect(viewport).toHaveAttribute('transform', 'translate(0,0) scale(1)');
    await page.mouse.move(box.x + 6, box.y + box.height - 6);
    await page.mouse.down();
    await page.mouse.move(box.x + 106, box.y + box.height - 46, { steps: 5 });
    await page.mouse.up();
    const moved = await viewport.getAttribute('transform');
    expect(moved).toMatch(/^translate\([1-9][\d.]*,-[1-9][\d.]*\) scale\(1\)$/);
  });

  test('run page reads a cut plan in full from the artifact bucket', async ({ page }) => {
    const calls = await mockApi(page);
    await page.goto(`/runs/${ids.run}`);
    await page.getByRole('button', { name: `Details of ${truncatedPlanStack}` }).click();
    const drawer = page.getByRole('dialog', { name: truncatedPlanStack });
    await expect(drawer.getByText("The full plan, from the server's artifact bucket.")).toBeVisible();
    await expect(drawer.locator('pre.plan')).toHaveText(fullPlanText);
    const planPath = `/v1/runs/${ids.run}/stacks/${encodeURIComponent(truncatedPlanStack)}/plan`;
    expect(calls.filter((c) => c.path === planPath)).toHaveLength(1);
  });

  test('run page keeps the stored beginning when the full plan is gone', async ({ page }) => {
    const message = 'the full plan text is no longer in the artifact bucket';
    await mockApi(page, (c) => (c.path.endsWith('/plan') ? { status: 404, body: { code: 'not_found', message } } : undefined));
    await page.goto(`/runs/${ids.run}`);
    await page.getByRole('button', { name: `Details of ${truncatedPlanStack}` }).click();
    const drawer = page.getByRole('dialog', { name: truncatedPlanStack });
    await expect(drawer.getByRole('status')).toContainText(`the full plan could not be loaded (${message})`);
    await expect(drawer.locator('pre.plan')).not.toContainText('Plan: 0 to add');
  });

  test('run page shows waves and opens a stack’s details', async ({ page }) => {
    await mockApi(page);
    await page.goto(`/runs/${ids.run}`);
    await expect(page.getByRole('heading', { level: 1 })).toHaveText(/Run 7d9f1b3c\s*failed/);
    await expect(page.getByRole('link', { name: '#42' })).toHaveAttribute('href', 'https://github.com/acme/infra/pull/42');
    for (const [wave, count] of [
      [0, 2],
      [1, 2],
      [2, 1],
    ] as const) {
      await expect(page.getByRole('region', { name: `Wave ${String(wave)}` }).getByRole('article')).toHaveCount(count);
    }
    const eks = page.getByRole('article', { name: 'stacks/prod/eks' });
    await expect(eks).toContainText('failed');
    await expect(eks.getByRole('link', { name: 'Job log' })).toHaveAttribute('href', /\/actions\/runs\/17438203\/job\//);
    await expect(page.getByRole('article', { name: 'stacks/prod/apps' })).toContainText('blocked');

    await eks.getByRole('button', { name: 'Details of stacks/prod/eks' }).click();
    const drawer = page.getByRole('dialog', { name: 'stacks/prod/eks' });
    await expect(drawer).toBeVisible();
    await expect(drawer.locator('pre.plan')).toContainText('Error: waiting for EKS Node Group (prod:default) update');
    await expect(drawer.getByRole('table', { name: 'Checks on stacks/prod/eks' })).toContainText('launch template replacement');
    await page.keyboard.press('Escape');
    await expect(drawer).toBeHidden();

    await page.getByRole('link', { name: 'View in graph' }).click();
    await expect(page).toHaveURL(`/repos/acme/infra?run=${ids.run}`);
    await expect(page.getByRole('region', { name: 'Affected set' })).toBeVisible();
  });

  test('re-run posts to the API and follows the new run', async ({ page }) => {
    const created = '0d1e2f3a-4b5c-4d6e-8f70-8192a3b4c5d6';
    const calls = await mockApi(page, (c) =>
      c.method === 'POST' && c.path.endsWith('/rerun') ? { body: { run_id: created, status: 'pending', existing: false } } : undefined,
    );
    await page.goto(`/runs/${ids.run}`);
    await page.getByRole('button', { name: 'Re-run' }).click();
    await expect(page).toHaveURL(`/runs/${created}`);
    expect(calls.find((c) => c.method === 'POST')).toMatchObject({ path: `/v1/runs/${ids.run}/rerun`, body: {} });
  });

  test('unlock asks for a reason, posts it and refreshes the stack', async ({ page }) => {
    let unlocked = false;
    await mockApi(page, (c) => {
      if (c.method === 'POST' && c.path === `/v1/stacks/${ids.stack}/unlock`) {
        unlocked = true;
        return { body: { released: [stack.lock] } };
      }
      if (unlocked && c.path === `/v1/stacks/${ids.stack}`) {
        const s: StackDetail = clone(stack);
        delete s.lock;
        return { body: s };
      }
      return undefined;
    });
    await page.goto(`/stacks/${ids.stack}`);
    const lock = page.getByRole('region', { name: 'Lock' });
    await expect(lock).toContainText('locked by PR #42');

    await lock.getByRole('button', { name: 'Unlock…' }).click();
    const dialog = page.getByRole('dialog', { name: 'Unlock stacks/prod/eks' });
    await expect(dialog).toBeVisible();
    await expect(dialog.getByLabel('Reason')).toBeFocused();
    await dialog.getByRole('button', { name: 'Unlock' }).click();
    await expect(dialog.getByRole('alert')).toContainText('Give a reason');

    await dialog.getByLabel('Reason').fill('Runner died mid-apply; state verified by hand');
    const [request] = await Promise.all([
      page.waitForRequest((r) => r.method() === 'POST' && r.url().endsWith(`/v1/stacks/${ids.stack}/unlock`)),
      dialog.getByRole('button', { name: 'Unlock' }).click(),
    ]);
    expect(request.postDataJSON()).toEqual({ reason: 'Runner died mid-apply; state verified by hand' });
    expect(request.headers()['content-type']).toBe('application/json');

    await expect(dialog).toBeHidden();
    await expect(lock).toContainText('Released 1 lock held by PR #42.');
    await expect(lock).toContainText('Not locked.');
  });

  test('module list filters and links to the module page', async ({ page }) => {
    await mockApi(page);
    await page.goto('/modules');
    const table = page.getByRole('table', { name: 'Modules' });
    await expect(table.getByRole('row')).toHaveCount(5);
    await page.getByLabel('Filter').fill('eks-addons');
    await expect(page).toHaveURL('/modules?q=eks-addons');
    await expect(table.getByRole('row')).toHaveCount(2);
    await table.getByRole('link', { name: 'acme/terraform-modules//eks-addons' }).click();
    await expect(page).toHaveURL(`/modules/${ids.module}`);
    await expect(page.getByRole('table', { name: /^Versions of/ }).getByRole('row')).toHaveCount(5);
    await expect(page.getByRole('table', { name: /^Stacks that use/ })).toContainText('2 versions behind');
  });

  test('loads nothing from outside the server except the GitHub avatar', async ({ page }) => {
    await mockApi(page);
    const origins = new Set<string>();
    page.on('request', (r) => origins.add(new URL(r.url()).origin));
    await page.goto('/repos/acme/infra');
    await expect(page.getByRole('group', { name: 'Dependency graph of acme/infra' })).toBeVisible();
    await page.goto(`/runs/${ids.run}`);
    await expect(page.getByRole('heading', { level: 1 })).toBeVisible();
    expect([...origins].sort()).toEqual(['http://localhost:4173', 'https://avatars.githubusercontent.com']);
  });

  test('follows the colour scheme', async ({ page }) => {
    await mockApi(page);
    await page.emulateMedia({ colorScheme: 'light' });
    await page.goto('/');
    await expect(page.getByRole('heading', { level: 1, name: 'Overview' })).toBeVisible();
    const light = await page.evaluate(() => getComputedStyle(document.documentElement).backgroundColor);
    await page.emulateMedia({ colorScheme: 'dark' });
    const dark = await page.evaluate(() => getComputedStyle(document.documentElement).backgroundColor);
    expect(light).toBe('rgb(246, 247, 249)');
    expect(dark).toBe('rgb(15, 18, 22)');
  });

  test('sign out returns to the sign-in screen', async ({ page }) => {
    const calls = await mockApi(page, (c) => (c.path === '/auth/logout' ? { status: 204 } : undefined));
    await page.goto('/');
    await page.getByRole('button', { name: 'Sign out' }).click();
    await expect(page.getByRole('heading', { name: 'Sign in to Stackorder' })).toBeVisible();
    expect(calls.some((c) => c.method === 'POST' && c.path === '/auth/logout')).toBe(true);
  });
});

test.describe('signed out', () => {
  test('shows only the sign-in link and fetches no data', async ({ page }) => {
    const calls = await mockApi(page, (c) =>
      c.path === '/v1/me' ? { status: 401, body: { code: 'unauthorized', message: 'no session' } } : undefined,
    );
    await page.goto(`/runs/${ids.run}`);
    await expect(page.getByRole('heading', { name: 'Sign in to Stackorder' })).toBeVisible();
    await expect(page.getByRole('link', { name: 'Sign in with GitHub' })).toHaveAttribute('href', '/auth/login');
    await expect(page.getByRole('navigation', { name: 'Main' })).toHaveCount(0);
    expect(calls.map((c) => c.path)).toEqual(['/v1/me']);
  });

  test('the sign-in link leaves the single-page app for the server', async ({ page }) => {
    await mockApi(page, (c) =>
      c.path === '/v1/me' ? { status: 401, body: { code: 'unauthorized', message: 'no session' } } : undefined,
    );
    await page.route('**/auth/login', (route) =>
      route.fulfill({ status: 200, contentType: 'text/html', body: '<h1>GitHub authorization</h1>' }),
    );
    await page.goto('/');
    await page.getByRole('link', { name: 'Sign in with GitHub' }).click();
    await expect(page).toHaveURL('/auth/login');
    await expect(page.getByRole('heading', { name: 'GitHub authorization' })).toBeVisible();
  });
});
