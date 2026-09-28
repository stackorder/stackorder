import { fireEvent, screen, waitFor, within } from '@testing-library/preact';
import { describe, expect, it, vi } from 'vitest';

import type { Run } from '../api/types';
import { clone, ids, run } from '../fixtures';
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

  it('shows not found for an unknown run', async () => {
    renderWithApp(<RunPage id="00000000-0000-4000-8000-000000000000" />, { url: '/runs/00000000-0000-4000-8000-000000000000' });
    expect(await screen.findByText('This run does not exist or you do not have access to it.')).toBeInTheDocument();
  });
});
