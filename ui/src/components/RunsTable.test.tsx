import { render, screen } from '@testing-library/preact';
import { describe, expect, it } from 'vitest';

import type { Run } from '../api/types';
import { RunStacks, RunsTable } from './RunsTable';

const run = (r: Partial<Run>): Run => ({
  id: '11111111-2222-4333-8444-555555555555',
  repo: 'acme/infra',
  sha: 'abc1234',
  trigger: 'comment',
  mode: 'apply',
  status: 'applied',
  created_at: '2026-09-28T09:00:00Z',
  waves: 1,
  current_wave: 0,
  ...r,
});

describe('RunStacks', () => {
  it.each([
    [{}, '—'],
    [{ stack_count: 1, stack_keys: ['stacks/a'] }, 'stacks/a'],
    [{ stack_count: 2, stack_keys: ['stacks/a', 'stacks/b'] }, 'stacks/a, stacks/b'],
    [{ stack_count: 12, stack_keys: ['stacks/a', 'stacks/b', 'stacks/c'] }, 'stacks/a, stacks/b +10 more'],
    [{ stack_count: 4 }, '4 stacks'],
  ])('renders %j as %s', (r, text) => {
    const { container } = render(<RunStacks run={r} />);
    expect(container).toHaveTextContent(text);
  });

  it('lists every key it has in the tooltip and marks the ones it lacks', () => {
    render(<RunStacks run={{ stack_count: 9, stack_keys: ['stacks/a', 'stacks/b', 'stacks/c'] }} />);
    expect(screen.getByTitle(/^stacks\/a/)).toHaveAttribute('title', 'stacks/a\nstacks/b\nstacks/c\n…');
  });
});

describe('RunsTable', () => {
  it('shows the wave of an apply in progress', () => {
    render(
      <RunsTable
        caption="Runs"
        runs={[
          run({ id: 'a', status: 'applying', waves: 3, current_wave: 1 }),
          run({ id: 'b', mode: 'plan', status: 'planning', waves: 3, current_wave: 0 }),
        ]}
      />,
    );
    expect(screen.getAllByText(/wave/)).toHaveLength(1);
    expect(screen.getByText('wave 1 of 3', { exact: false })).toBeInTheDocument();
  });

  it('shows how long a finished run took and a dash for one still going', () => {
    render(
      <RunsTable
        caption="Runs"
        runs={[
          run({ id: 'a', started_at: '2026-09-28T09:00:05Z', finished_at: '2026-09-28T09:04:17Z' }),
          run({ id: 'b', status: 'applying', started_at: '2026-09-28T09:00:05Z' }),
        ]}
      />,
    );
    expect(screen.getByText('4m 12s')).toBeInTheDocument();
    expect(screen.getAllByText('—').length).toBeGreaterThan(0);
  });
});
