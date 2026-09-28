import { fireEvent, screen, waitFor, within } from '@testing-library/preact';
import { describe, expect, it } from 'vitest';

import { overview } from '../fixtures';
import { json, nth, renderWithApp } from '../test/render';
import { OverviewPage } from './OverviewPage';

function stat(label: string): string | null {
  const term = screen.getByText(label, { selector: 'dt' });
  return term.nextElementSibling?.textContent ?? null;
}

describe('OverviewPage', () => {
  it('shows counts, statuses, drift, locks and recent runs', async () => {
    renderWithApp(<OverviewPage />);
    expect(await screen.findByRole('heading', { level: 1, name: 'Overview' })).toBeInTheDocument();
    expect(stat('Repositories')).toBe('3');
    expect(stat('Stacks')).toBe('14');
    expect(stat('Drifted')).toBe('2');
    expect(stat('Locks held')).toBe('5');
    expect(screen.getByRole('link', { name: '3' })).toHaveAttribute('href', '/repos');

    const stacks = within(screen.getByRole('region', { name: 'Stacks by status' }));
    expect(stacks.getAllByRole('listitem').map((li) => li.textContent)).toEqual([
      'planned1',
      'applied9',
      'failed1',
      'blocked1',
      'noop1',
      'unknown1',
    ]);
    const runs = within(screen.getByRole('region', { name: 'Runs by status' }));
    expect(runs.getAllByRole('listitem').map((li) => li.textContent)).toEqual([
      'planning1',
      'planned6',
      'applied31',
      'failed2',
      'superseded9',
    ]);

    const table = screen.getByRole('table', { name: 'Recent runs' });
    const rows = within(table).getAllByRole('row').slice(1);
    expect(rows).toHaveLength(overview.recent_runs?.length ?? 0);
    const failed = rows[1];
    if (!failed) throw new Error('no second row');
    expect(within(failed).getByRole('link', { name: '7d9f1b3c' })).toHaveAttribute(
      'href',
      '/runs/7d9f1b3c-5e7a-4b9c-8d1e-3f5a7b9c1d66',
    );
    expect(within(failed).getByRole('link', { name: 'acme/infra' })).toHaveAttribute('href', '/repos/acme/infra');
    expect(within(failed).getByText('apply · PR #42')).toBeInTheDocument();
    expect(within(failed).getByText('failed')).toHaveClass('badge--danger');
    expect(within(failed).getByText('octocat')).toBeInTheDocument();
    expect(within(nth(rows, 3)).getByText('scheduler')).toBeInTheDocument();
    expect(document.title).toBe('Overview · Stackorder');
  });

  it('shows empty breakdowns and no runs', async () => {
    renderWithApp(<OverviewPage />, {
      handler: (c) =>
        c.path === '/v1/overview'
          ? json({ repos: 0, stacks: 0, drifted: 0, locks_held: 0, runs_by_status: null, stacks_by_status: {} })
          : undefined,
    });
    expect(await screen.findByText('No runs yet.')).toBeInTheDocument();
    expect(screen.getAllByText('None yet.')).toHaveLength(2);
  });

  it('shows an error and retries', async () => {
    let fail = true;
    const { calls } = renderWithApp(<OverviewPage />, {
      handler: (c) => (c.path === '/v1/overview' && fail ? json({ code: 'internal', message: 'database is down' }, 500) : undefined),
    });
    expect(await screen.findByText('database is down')).toBeInTheDocument();
    fail = false;
    fireEvent.click(screen.getByRole('button', { name: 'Try again' }));
    expect(await screen.findByRole('heading', { level: 1, name: 'Overview' })).toBeInTheDocument();
    await waitFor(() => {
      expect(calls.filter((c) => c.path === '/v1/overview')).toHaveLength(2);
    });
  });
});
