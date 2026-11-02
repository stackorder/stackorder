import { fireEvent, screen, waitFor, within } from '@testing-library/preact';
import { describe, expect, it } from 'vitest';

import { clone, ids, stack, stacks } from '../fixtures';
import { json, nth, paged, renderWithApp } from '../test/render';
import { matchesStack, StacksPage } from './StacksPage';

function cells(row: HTMLElement): (string | null)[] {
  return [...row.querySelectorAll('td')].map((td) => td.textContent);
}

describe('matchesStack', () => {
  it.each([
    ['', true],
    ['PROD/EKS', true],
    ['acme/infra', true],
    ['stacks/prod', true],
    ['production', true],
    ['  eks ', true],
    ['staging', false],
    ['network', false],
    ['default', false],
  ])('filter %j matches: %s', (q, want) => {
    expect(matchesStack(stack, q)).toBe(want);
  });

  it('does not match an environment the stack does not have', () => {
    expect(matchesStack({ key: 'stacks/shared/iam', repo: 'acme/platform-infra', path: 'stacks/shared/iam' }, 'prod')).toBe(false);
  });

  it('matches a stack without an environment as default', () => {
    expect(matchesStack({ key: 'stacks/shared/iam', repo: 'acme/platform-infra', path: 'stacks/shared/iam' }, 'Default')).toBe(true);
  });
});

describe('StacksPage', () => {
  it('lists every stack with its repository, environment, last apply, drift and lock', async () => {
    renderWithApp(<StacksPage />, { url: '/stacks' });
    expect(await screen.findByRole('heading', { level: 1, name: 'Stacks' })).toBeInTheDocument();
    const table = screen.getByRole('table', { name: 'Stacks' });
    expect(within(table).getAllByRole('columnheader').map((th) => th.textContent)).toEqual([
      'Stack',
      'Repository',
      'Environment',
      'Last apply',
      'Drift',
      'Lock',
    ]);
    const rows = within(table).getAllByRole('row').slice(1);
    expect(rows).toHaveLength(14);
    expect(rows.map((r) => cells(r).slice(0, 2))).toEqual((stacks.items ?? []).map((s) => [s.key, s.repo]));
    expect(screen.getByText('14 of 14')).toBeInTheDocument();
    expect(document.title).toBe('Stacks · Stackorder');

    const eks = nth(rows, 1);
    expect(within(eks).getByRole('link', { name: 'stacks/prod/eks' })).toHaveAttribute('href', `/stacks/${ids.stack}`);
    expect(within(eks).getByRole('link', { name: 'acme/infra' })).toHaveAttribute('href', '/repos/acme/infra');
    expect(within(eks).getByText('production')).toBeInTheDocument();
    expect(within(eks).getByText('applied')).toHaveClass('badge--success');
    const applied = nth(within(eks).getAllByRole('link'), 2);
    expect(applied).toHaveAttribute('href', `/runs/${stack.last_apply?.run_id ?? ''}`);
    expect(applied.querySelector('time')).toHaveAttribute('datetime', stack.last_apply?.finished_at);
    expect(within(eks).getByText('drifted')).toHaveClass('badge--warning');
    expect(within(eks).getByText('locked')).toHaveClass('badge--running');

    const apps = nth(rows, 0);
    expect(within(apps).getByText('in sync')).toHaveClass('badge--success');
    expect(cells(apps).slice(3)).toEqual(['—', 'in sync', 'locked']);

    const iam = nth(rows, 13);
    expect(cells(iam)).toEqual(['stacks/shared/iam', 'acme/platform-infra', 'default', '—', '—', '—']);
    expect(within(iam).getByRole('link', { name: 'acme/platform-infra' })).toHaveAttribute('href', '/repos/acme/platform-infra');

    const dns = within(table).getByRole('link', { name: 'stacks/edge/dns' }).closest('tr');
    if (!dns) throw new Error('no row for stacks/edge/dns');
    expect(within(dns).getByText('noop')).toHaveClass('badge--muted');
  });

  it('links to the run of an apply that has not finished', async () => {
    const s = clone(stack);
    s.last_apply = { run_id: ids.run, sha: s.last_apply?.sha ?? '', status: 'applying' };
    renderWithApp(<StacksPage />, {
      url: '/stacks',
      handler: (c) => (c.path === '/v1/stacks' ? json({ items: [s], total: 1 }) : undefined),
    });
    const table = await screen.findByRole('table', { name: 'Stacks' });
    expect(within(table).getByText('applying')).toHaveClass('badge--running');
    expect(within(table).getByRole('link', { name: '7d9f1b3c' })).toHaveAttribute('href', `/runs/${ids.run}`);
  });

  it('lists every stack past the first page', async () => {
    const many = Array.from({ length: 70 }, (_, i) => ({
      id: `s${String(i)}`,
      repo: 'acme/infra',
      key: `stacks/s${String(i).padStart(2, '0')}`,
      path: `stacks/s${String(i).padStart(2, '0')}`,
    }));
    renderWithApp(<StacksPage />, {
      url: '/stacks?q=s65',
      handler: (c) => (c.path === '/v1/stacks' ? paged(many, c) : undefined),
    });
    const table = await screen.findByRole('table', { name: 'Stacks' });
    expect(within(table).getByRole('link', { name: 'stacks/s65' })).toHaveAttribute('href', '/stacks/s65');
    expect(screen.getByText('1 of 70')).toBeInTheDocument();
  });

  it('filters from the URL and as the user types', async () => {
    renderWithApp(<StacksPage />, { url: '/stacks?q=network' });
    const table = await screen.findByRole('table', { name: 'Stacks' });
    expect(within(table).getAllByRole('row')).toHaveLength(7);
    expect(screen.getByText('6 of 14')).toBeInTheDocument();
    expect(screen.getByLabelText('Filter')).toHaveValue('network');

    fireEvent.input(screen.getByLabelText('Filter'), { target: { value: 'Staging' } });
    await waitFor(() => {
      expect(window.location.search).toBe('?q=Staging');
    });
    expect(await screen.findByText('3 of 14')).toBeInTheDocument();
    expect(
      within(screen.getByRole('table', { name: 'Stacks' }))
        .getAllByRole('link', { name: /^stacks\// })
        .map((a) => a.textContent),
    ).toEqual(['stacks/staging/eks', 'stacks/staging/vpc', 'stacks/transit/staging']);

    fireEvent.input(screen.getByLabelText('Filter'), { target: { value: 'default' } });
    await waitFor(() => {
      expect(
        within(screen.getByRole('table', { name: 'Stacks' }))
          .getAllByRole('link', { name: /^stacks\// })
          .map((a) => a.textContent),
      ).toEqual(['stacks/transit/hub', 'stacks/shared/eks', 'stacks/shared/iam']);
    });
    expect(screen.getByText('3 of 14')).toBeInTheDocument();

    fireEvent.input(screen.getByLabelText('Filter'), { target: { value: 'nothing-matches' } });
    expect(await screen.findByText('No stack matches the filter.')).toBeInTheDocument();
    expect(screen.getByText('0 of 14')).toBeInTheDocument();

    fireEvent.input(screen.getByLabelText('Filter'), { target: { value: '' } });
    await waitFor(() => {
      expect(window.location.search).toBe('');
    });
    expect(await screen.findByText('14 of 14')).toBeInTheDocument();
  });

  it('explains when stacks appear', async () => {
    renderWithApp(<StacksPage />, { url: '/stacks', handler: (c) => (c.path === '/v1/stacks' ? json({ items: [] }) : undefined) });
    expect(await screen.findByText(/once Stackorder has scanned a repository/)).toBeInTheDocument();
    expect(screen.queryByRole('table')).not.toBeInTheDocument();
    expect(screen.getByText('0 of 0')).toBeInTheDocument();
  });

  it('shows an error and retries', async () => {
    let fail = true;
    const { calls } = renderWithApp(<StacksPage />, {
      url: '/stacks',
      handler: (c) => (c.path === '/v1/stacks' && fail ? json({ code: 'internal', message: 'database is down' }, 500) : undefined),
    });
    expect(await screen.findByText('database is down')).toBeInTheDocument();
    fail = false;
    fireEvent.click(screen.getByRole('button', { name: 'Try again' }));
    expect(await screen.findByRole('table', { name: 'Stacks' })).toBeInTheDocument();
    await waitFor(() => {
      expect(calls.filter((c) => c.path === '/v1/stacks')).toHaveLength(2);
    });
  });
});
