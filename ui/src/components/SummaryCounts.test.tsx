import { render, screen } from '@testing-library/preact';
import { describe, expect, it } from 'vitest';

import type { PlanSummary } from '../api/types';
import { describeSummary, SummaryCounts } from './SummaryCounts';

const summary = (s: Partial<PlanSummary>): PlanSummary => ({ adds: 0, changes: 0, destroys: 0, replaces: 0, ...s });

describe('describeSummary', () => {
  it.each<[Partial<PlanSummary>, string]>([
    [{ adds: 3, changes: 1 }, '3 to add, 1 to change, 0 to destroy'],
    [{ destroys: 2, replaces: 1 }, '0 to add, 0 to change, 2 to destroy, 1 to replace'],
    [{ imports: 2, moves: 1 }, '0 to add, 0 to change, 0 to destroy, 2 to import, 1 to move'],
    [{ output_changes: 1 }, '0 to add, 0 to change, 0 to destroy, 1 output change'],
    [{ output_changes: 3 }, '0 to add, 0 to change, 0 to destroy, 3 output changes'],
  ])('%j', (s, want) => {
    expect(describeSummary(summary(s))).toBe(want);
  });
});

describe('SummaryCounts', () => {
  it('shows +~- counts with an accessible description', () => {
    render(<SummaryCounts summary={summary({ adds: 1, changes: 2, destroys: 3 })} />);
    expect(screen.getByText('+1')).toHaveClass('count--add');
    expect(screen.getByText('~2')).toHaveClass('count--change');
    expect(screen.getByText('-3')).toHaveClass('count--destroy');
    expect(screen.queryByText(/±/)).not.toBeInTheDocument();
    expect(screen.getByText('1 to add, 2 to change, 3 to destroy')).toHaveClass('sr-only');
  });

  it('shows replacements when there are any', () => {
    render(<SummaryCounts summary={summary({ changes: 1, replaces: 1 })} />);
    expect(screen.getByText('±1')).toHaveClass('count--replace');
  });

  it('shows imports and moves', () => {
    render(<SummaryCounts summary={summary({ imports: 2, moves: 1 })} />);
    expect(screen.getByText('2 imports')).toBeInTheDocument();
    expect(screen.getByText('1 move')).toBeInTheDocument();
  });

  it('says when a plan is empty', () => {
    render(<SummaryCounts summary={summary({})} />);
    expect(screen.getByText('No changes')).toBeInTheDocument();
  });

  it('counts output-only plans as changes', () => {
    render(<SummaryCounts summary={summary({ output_changes: 2 })} />);
    expect(screen.getByText('2 outputs')).toBeInTheDocument();
    expect(screen.queryByText('No changes')).not.toBeInTheDocument();
  });

  it('marks a missing summary', () => {
    render(<SummaryCounts summary={undefined} />);
    expect(screen.getByText('No plan summary')).toBeInTheDocument();
  });
});
