import { fireEvent, screen, waitFor, within } from '@testing-library/preact';
import { describe, expect, it, vi } from 'vitest';

import { Routes } from '../app';
import type { Run } from '../api/types';
import { clone, fullPlanText, ids, run, truncatedPlanStack } from '../fixtures';
import { json, renderWithApp } from '../test/render';
import { REFRESH_MS, RunPage, runWaves } from './RunPage';

const url = `/runs/${ids.run}`;
const page = <RunPage id={ids.run} />;

describe('runWaves', () => {
  it.each<[string, Pick<Run, 'waves' | 'stacks'>, string[][]]>([
    ['the fixture run', run, [['stacks/prod/vpc', 'stacks/staging/vpc'], ['stacks/prod/eks', 'stacks/staging/eks'], ['stacks/prod/apps']]],
    ['declared waves with no stacks yet', { waves: 2 }, [[], []]],
    [
      'stacks beyond the declared count',
      { waves: 1, stacks: [{ stack_id: 'x', key: 'b', path: 'b', wave: 1, status: 'pending' }] },
      [[], ['b']],
    ],
  ])('%s', (_name, r, want) => {
    expect(runWaves(r).map((w) => w.stacks.map((s) => s.key))).toEqual(want);
  });
});

describe('RunPage', () => {
  it('shows the status header with PR, commit and graph links', async () => {
    renderWithApp(page, { url });
    const heading = await screen.findByRole('heading', { level: 1 });
    expect(heading).toHaveTextContent('Run 7d9f1b3c failed');
    expect(within(heading).getByText('failed')).toHaveClass('badge--danger');
    expect(screen.getByText('apply · PR #42')).toBeInTheDocument();
    expect(screen.getByRole('link', { name: '#42' })).toHaveAttribute('href', 'https://github.com/acme/infra/pull/42');
    expect(screen.getByRole('link', { name: '4f9c2d1' })).toHaveAttribute(
      'href',
      'https://github.com/acme/infra/commit/4f9c2d1e8b7a6c5d3e2f1a0b9c8d7e6f5a4b3c2d',
    );
    expect(screen.getByRole('link', { name: 'View in graph' })).toHaveAttribute('href', `/repos/acme/infra?run=${ids.run}`);
    expect(screen.getByText('15m 2s')).toBeInTheDocument();
    expect(screen.getByRole('list', { name: 'Run warnings' })).toHaveTextContent('stacks/prod/apps is blocked');
    expect(screen.queryByText(/Refreshing every/)).not.toBeInTheDocument();
  });

  it('links the PR and commit on the GitHub host of the job logs, not the run page URL', async () => {
    const ghes = clone(run);
    for (const s of ghes.stacks ?? []) if (s.job_url) s.job_url = s.job_url.replace('https://github.com', 'https://ghe.acme.dev');
    renderWithApp(page, { url, handler: (c) => (c.path === `/v1/runs/${ids.run}` ? json(ghes) : undefined) });
    expect(await screen.findByRole('link', { name: '#42' })).toHaveAttribute('href', 'https://ghe.acme.dev/acme/infra/pull/42');
    expect(screen.getByRole('link', { name: '4f9c2d1' })).toHaveAttribute(
      'href',
      'https://ghe.acme.dev/acme/infra/commit/4f9c2d1e8b7a6c5d3e2f1a0b9c8d7e6f5a4b3c2d',
    );
    for (const a of document.querySelectorAll('a[href]')) {
      expect(a.getAttribute('href')).not.toContain('stackorder.example.com');
    }
  });

  it('lays out the three waves with per-stack results and job logs', async () => {
    renderWithApp(page, { url });
    await screen.findByRole('heading', { level: 1 });
    const wave = (n: number) => within(screen.getByRole('region', { name: `Wave ${String(n)}` }));
    expect(wave(0).getAllByRole('article').map((a) => a.querySelector('h3')?.textContent)).toEqual([
      'stacks/prod/vpc',
      'stacks/staging/vpc',
    ]);
    const eks = within(wave(1).getByRole('article', { name: 'stacks/prod/eks' }));
    expect(eks.getByText('failed')).toBeInTheDocument();
    expect(eks.getByText('0 to add, 1 to change, 0 to destroy, 1 to replace')).toBeInTheDocument();
    expect(eks.getByRole('link', { name: 'Job log' })).toHaveAttribute(
      'href',
      'https://github.com/acme/infra/actions/runs/17438203/job/48902313',
    );
    expect(eks.getByRole('link', { name: 'stacks/prod/eks' })).toHaveAttribute('href', `/stacks/${ids.stack}`);
    const apps = within(wave(2).getByRole('article', { name: 'stacks/prod/apps' }));
    expect(apps.getByText('blocked')).toHaveClass('badge--warning');
    expect(apps.getByText('No changes')).toBeInTheDocument();
    expect(apps.queryByRole('link', { name: 'Job log' })).not.toBeInTheDocument();
    expect(apps.getByText('production · reads state')).toBeInTheDocument();
  });

  it('opens the details drawer with the plan, checks and resources', async () => {
    renderWithApp(page, { url });
    fireEvent.click(await screen.findByRole('button', { name: 'Details of stacks/prod/eks' }));
    const drawer = screen.getByRole('dialog', { name: 'stacks/prod/eks' });
    const plan = within(drawer).getByLabelText('Plan output of stacks/prod/eks');
    expect(plan.tagName).toBe('PRE');
    expect(plan.textContent).toContain('Error: waiting for EKS Node Group (prod:default) update');
    expect(within(drawer).getByText('module.eks.aws_launch_template.nodes')).toBeInTheDocument();
    const checks = within(drawer).getByRole('table', { name: 'Checks on stacks/prod/eks' });
    expect(within(checks).getByText('warn')).toHaveClass('badge--warning');
    expect(within(checks).getByRole('link', { name: '+$62.40/month' })).toHaveAttribute(
      'href',
      'https://dashboard.infracost.io/org/acme/runs/7732',
    );
    expect(within(drawer).getByText('Exit code').nextElementSibling).toHaveTextContent('1');
    expect(within(drawer).getByText('11m 47s')).toBeInTheDocument();
    fireEvent.click(within(drawer).getByRole('button', { name: 'Close' }));
    await waitFor(() => {
      expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
    });
  });

  it('explains why a stack has no plan text', async () => {
    renderWithApp(page, { url });
    fireEvent.click(await screen.findByRole('button', { name: 'Details of stacks/prod/apps' }));
    const drawer = screen.getByRole('dialog', { name: 'stacks/prod/apps' });
    expect(within(drawer).getByText(/No plan text was recorded/)).toBeInTheDocument();
  });

  it('warns when the plan text was truncated', async () => {
    const truncated = clone(run);
    const first = truncated.stacks?.[0];
    if (first) first.truncated = true;
    renderWithApp(page, { url, handler: (c) => (c.path === url.replace('/runs', '/v1/runs') ? json(truncated) : undefined) });
    fireEvent.click(await screen.findByRole('button', { name: 'Details of stacks/prod/vpc' }));
    expect(screen.getByText(/truncated at 256 KB/)).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'The job log has the full plan.' })).toBeInTheDocument();
  });

  it('loads the full plan from the artifact bucket when the stored text was cut', async () => {
    const { calls } = renderWithApp(page, { url });
    fireEvent.click(await screen.findByRole('button', { name: `Details of ${truncatedPlanStack}` }));
    const drawer = screen.getByRole('dialog', { name: truncatedPlanStack });
    expect(await within(drawer).findByText("The full plan, from the server's artifact bucket.")).toBeInTheDocument();
    expect(within(drawer).getByLabelText(`Plan output of ${truncatedPlanStack}`).textContent).toBe(fullPlanText);
    expect(within(drawer).queryByText(/truncated at 256 KB/)).not.toBeInTheDocument();
    const planPath = `/v1/runs/${ids.run}/stacks/${encodeURIComponent(truncatedPlanStack)}/plan`;
    expect(calls.filter((c) => c.path === planPath)).toHaveLength(1);

    fireEvent.click(within(drawer).getByRole('button', { name: 'Close' }));
    fireEvent.click(screen.getByRole('button', { name: 'Details of stacks/prod/vpc' }));
    const vpcDrawer = screen.getByRole('dialog', { name: 'stacks/prod/vpc' });
    expect(within(vpcDrawer).getByLabelText('Plan output of stacks/prod/vpc')).toHaveTextContent('Plan: 0 to add, 2 to change, 0 to destroy.');
    expect(calls.filter((c) => c.path.endsWith('/plan'))).toHaveLength(1);
  });

  it('keeps the stored beginning when the full plan cannot be loaded', async () => {
    const message = `the full plan text of ${truncatedPlanStack} in run ${ids.run} is no longer in the artifact bucket`;
    renderWithApp(page, {
      url,
      handler: (c) => (c.path.endsWith('/plan') ? json({ code: 'not_found', message }, 404) : undefined),
    });
    fireEvent.click(await screen.findByRole('button', { name: `Details of ${truncatedPlanStack}` }));
    const drawer = screen.getByRole('dialog', { name: truncatedPlanStack });
    expect(await within(drawer).findByText(/the full plan could not be loaded/)).toHaveTextContent(message);
    expect(within(drawer).getByRole('link', { name: 'The job log has the full plan.' })).toBeInTheDocument();
    expect(within(drawer).getByLabelText(`Plan output of ${truncatedPlanStack}`).textContent).not.toContain('Plan: 0 to add');
  });

  it('loads the full plan when Postgres no longer keeps its beginning', async () => {
    const pruned = clone(run);
    const cut = pruned.stacks?.find((s) => s.key === truncatedPlanStack);
    if (cut) delete cut.plan_text;
    renderWithApp(page, { url, handler: (c) => (c.path === `/v1/runs/${ids.run}` ? json(pruned) : undefined) });
    fireEvent.click(await screen.findByRole('button', { name: `Details of ${truncatedPlanStack}` }));
    const drawer = screen.getByRole('dialog', { name: truncatedPlanStack });
    expect(within(drawer).getByRole('status')).toHaveTextContent("Loading the full plan from the server's artifact bucket");
    expect(within(drawer).queryByText(/No plan text was recorded/)).not.toBeInTheDocument();
    expect(await within(drawer).findByLabelText(`Plan output of ${truncatedPlanStack}`)).toHaveTextContent(
      'Plan: 0 to add, 1 to change, 0 to destroy.',
    );
    expect(within(drawer).queryByRole('status')).not.toBeInTheDocument();
  });

  it('says why nothing is shown when neither Postgres nor the bucket has the plan', async () => {
    const pruned = clone(run);
    const cut = pruned.stacks?.find((s) => s.key === truncatedPlanStack);
    if (cut) delete cut.plan_text;
    renderWithApp(page, {
      url,
      handler: (c) => {
        if (c.path === `/v1/runs/${ids.run}`) return json(pruned);
        return c.path.endsWith('/plan') ? json({ code: 'not_found', message: 'no longer in the artifact bucket' }, 404) : undefined;
      },
    });
    fireEvent.click(await screen.findByRole('button', { name: `Details of ${truncatedPlanStack}` }));
    const drawer = screen.getByRole('dialog', { name: truncatedPlanStack });
    expect(await within(drawer).findByText(/^The full plan could not be loaded/)).toHaveTextContent(
      'The full plan could not be loaded (no longer in the artifact bucket).',
    );
    expect(within(drawer).queryByLabelText(`Plan output of ${truncatedPlanStack}`)).not.toBeInTheDocument();
    expect(within(drawer).queryByText(/No plan text was recorded/)).not.toBeInTheDocument();
  });

  it('reads the full plan once while an in-flight run refreshes', async () => {
    const setIntervalSpy = vi.spyOn(globalThis, 'setInterval');
    const live = clone(run);
    live.status = 'applying';
    live.current_wave = 1;
    const later = clone(live);
    later.current_wave = 2;
    let refreshed = false;
    const { calls } = renderWithApp(page, {
      url,
      handler: (c) => (c.path === `/v1/runs/${ids.run}` ? json(refreshed ? later : live) : undefined),
    });
    fireEvent.click(await screen.findByRole('button', { name: `Details of ${truncatedPlanStack}` }));
    const drawer = screen.getByRole('dialog', { name: truncatedPlanStack });
    expect(await within(drawer).findByText("The full plan, from the server's artifact bucket.")).toBeInTheDocument();
    await waitFor(() => {
      expect(setIntervalSpy.mock.calls.some(([, ms]) => ms === REFRESH_MS)).toBe(true);
    });
    const tick = setIntervalSpy.mock.calls.find(([, ms]) => ms === REFRESH_MS)?.[0] as () => void;
    refreshed = true;
    tick();
    expect(await within(screen.getByRole('region', { name: /Wave 2/ })).findByText('current')).toBeInTheDocument();
    await new Promise((resolve) => setTimeout(resolve, 50));
    expect(within(screen.getByRole('dialog', { name: truncatedPlanStack })).getByLabelText(`Plan output of ${truncatedPlanStack}`).textContent).toBe(
      fullPlanText,
    );
    expect(calls.filter((c) => c.path.endsWith('/plan'))).toHaveLength(1);
  });

  it('never turns a runner-reported URL with another scheme into a link', async () => {
    const hostile = clone(run);
    hostile.html_url = 'javascript:alert(1)';
    for (const s of hostile.stacks ?? []) {
      s.job_url = 'javascript:alert(2)';
      for (const c of s.checks ?? []) if (c.details_url) c.details_url = ' javascript:alert(document.cookie)';
    }
    renderWithApp(page, { url, handler: (c) => (c.path === `/v1/runs/${ids.run}` ? json(hostile) : undefined) });
    fireEvent.click(await screen.findByRole('button', { name: 'Details of stacks/prod/eks' }));
    const drawer = screen.getByRole('dialog', { name: 'stacks/prod/eks' });
    expect(within(drawer).getByText('+$62.40/month')).not.toHaveAttribute('href');
    expect(screen.queryByRole('link', { name: 'Open on GitHub' })).not.toBeInTheDocument();
    expect(screen.queryAllByRole('link', { name: 'Job log' })).toHaveLength(0);
    for (const a of document.querySelectorAll('a[href]')) {
      expect(a.getAttribute('href')).toMatch(/^(\/|https?:\/\/)/);
    }
  });

  it('re-runs and moves to the new run', async () => {
    const created = '0d1e2f3a-4b5c-4d6e-8f70-8192a3b4c5d6';
    const { calls } = renderWithApp(page, {
      url,
      handler: (c) =>
        c.method === 'POST' ? json({ run_id: created, status: 'pending', existing: false }) : undefined,
    });
    fireEvent.click(await screen.findByRole('button', { name: 'Re-run' }));
    await waitFor(() => {
      expect(window.location.pathname).toBe(`/runs/${created}`);
    });
    expect(calls.find((c) => c.method === 'POST')).toMatchObject({ path: `/v1/runs/${ids.run}/rerun`, body: {} });
  });

  it('reports a refused re-run', async () => {
    renderWithApp(page, {
      url,
      handler: (c) => (c.method === 'POST' ? json({ code: 'conflict', message: 'a newer run exists for PR #42' }, 409) : undefined),
    });
    fireEvent.click(await screen.findByRole('button', { name: 'Re-run' }));
    expect(await screen.findByRole('alert')).toHaveTextContent('Re-run failed: a newer run exists for PR #42');
  });

  it('refreshes every 10 s while the run is in flight and stops once it is terminal', async () => {
    const setIntervalSpy = vi.spyOn(globalThis, 'setInterval');
    const clearIntervalSpy = vi.spyOn(globalThis, 'clearInterval');
    const live = clone(run);
    live.status = 'applying';
    live.current_wave = 1;
    let terminal = false;
    const { calls } = renderWithApp(page, {
      url,
      handler: (c) => (c.path === `/v1/runs/${ids.run}` ? json(terminal ? run : live) : undefined),
    });
    expect(await screen.findByText('Refreshing every 10 s')).toBeInTheDocument();
    expect(within(screen.getByRole('region', { name: /Wave 1/ })).getByText('current')).toBeInTheDocument();
    const gets = () => calls.filter((c) => c.path === `/v1/runs/${ids.run}`).length;
    const refreshes = () => setIntervalSpy.mock.calls.filter(([, ms]) => ms === REFRESH_MS);
    await waitFor(() => {
      expect(refreshes()).toHaveLength(1);
    });
    const index = setIntervalSpy.mock.calls.findIndex(([, ms]) => ms === REFRESH_MS);
    const tick = setIntervalSpy.mock.calls[index]?.[0] as () => void;
    const timer: unknown = setIntervalSpy.mock.results[index]?.value;
    expect(gets()).toBe(1);

    tick();
    await waitFor(() => {
      expect(gets()).toBe(2);
    });
    terminal = true;
    tick();
    await waitFor(() => {
      expect(gets()).toBe(3);
    });
    await screen.findByText('failed', { selector: 'h1 .badge' });
    await waitFor(() => {
      expect(clearIntervalSpy).toHaveBeenCalledWith(timer);
    });
    expect(screen.queryByText('Refreshing every 10 s')).not.toBeInTheDocument();
    expect(refreshes()).toHaveLength(1);
  });

  it('starts clean on the next run after going back in history', async () => {
    renderWithApp(<Routes />, {
      url: `/runs/${ids.planRun}`,
      handler: (c) => (c.method === 'POST' ? new Response(null, { status: 202 }) : undefined),
    });
    fireEvent.click(await screen.findByRole('button', { name: 'Re-run' }));
    expect(await screen.findByText('Re-run requested.')).toBeInTheDocument();
    window.history.pushState(null, '', url);
    window.dispatchEvent(new PopStateEvent('popstate'));
    expect(await screen.findByRole('heading', { level: 1, name: /^Run 7d9f1b3c/ })).toHaveTextContent('failed');
    fireEvent.click(screen.getByRole('button', { name: 'Details of stacks/prod/eks' }));
    expect(screen.getByRole('dialog', { name: 'stacks/prod/eks' })).toBeInTheDocument();
    expect(screen.queryByText('Re-run requested.')).not.toBeInTheDocument();

    window.history.back();
    await waitFor(() => {
      expect(window.location.pathname).toBe(`/runs/${ids.planRun}`);
    });
    expect(await screen.findByRole('heading', { level: 1, name: /^Run 2c4e6a80/ })).toHaveTextContent('planned');
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
  });

  it('stops refreshing once the session has ended and offers to sign in again', async () => {
    const setIntervalSpy = vi.spyOn(globalThis, 'setInterval');
    const clearIntervalSpy = vi.spyOn(globalThis, 'clearInterval');
    const live = clone(run);
    live.status = 'applying';
    let expired = false;
    renderWithApp(page, {
      url,
      handler: (c) =>
        c.path === `/v1/runs/${ids.run}`
          ? expired
            ? json({ code: 'unauthorized', message: 'session expired' }, 401)
            : json(live)
          : undefined,
    });
    expect(await screen.findByText('Refreshing every 10 s')).toBeInTheDocument();
    await waitFor(() => {
      expect(setIntervalSpy.mock.calls.some(([, ms]) => ms === REFRESH_MS)).toBe(true);
    });
    const index = setIntervalSpy.mock.calls.findIndex(([, ms]) => ms === REFRESH_MS);
    const tick = setIntervalSpy.mock.calls[index]?.[0] as () => void;
    const timer: unknown = setIntervalSpy.mock.results[index]?.value;

    expired = true;
    tick();
    expect(await screen.findByText(/Refresh failed: session expired/)).toBeInTheDocument();
    await waitFor(() => {
      expect(clearIntervalSpy).toHaveBeenCalledWith(timer);
    });
    expect(screen.queryByText('Refreshing every 10 s')).not.toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Sign in again' })).toHaveAttribute('href', '/auth/login');
    expect(setIntervalSpy.mock.calls.filter(([, ms]) => ms === REFRESH_MS)).toHaveLength(1);
  });

  it('keeps refreshing through a transient failure', async () => {
    const setIntervalSpy = vi.spyOn(globalThis, 'setInterval');
    const live = clone(run);
    live.status = 'applying';
    let down = false;
    renderWithApp(page, {
      url,
      handler: (c) =>
        c.path === `/v1/runs/${ids.run}` ? (down ? json({ code: 'internal', message: 'database unavailable' }, 503) : json(live)) : undefined,
    });
    expect(await screen.findByText('Refreshing every 10 s')).toBeInTheDocument();
    await waitFor(() => {
      expect(setIntervalSpy.mock.calls.some(([, ms]) => ms === REFRESH_MS)).toBe(true);
    });
    const tick = setIntervalSpy.mock.calls.find(([, ms]) => ms === REFRESH_MS)?.[0] as () => void;
    down = true;
    tick();
    expect(await screen.findByText(/Refresh failed: database unavailable/)).toBeInTheDocument();
    expect(screen.getByText('Refreshing every 10 s')).toBeInTheDocument();
    down = false;
    tick();
    await waitFor(() => {
      expect(screen.queryByText(/Refresh failed/)).not.toBeInTheDocument();
    });
  });

  it('shows not found for an unknown run', async () => {
    renderWithApp(<RunPage id="00000000-0000-4000-8000-000000000000" />, { url: '/runs/00000000-0000-4000-8000-000000000000' });
    expect(await screen.findByText('This run does not exist or you do not have access to it.')).toBeInTheDocument();
  });
});
