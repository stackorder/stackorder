import { fireEvent, render, screen } from '@testing-library/preact';
import { describe, expect, it, vi } from 'vitest';

import { ApiError, NETWORK_ERROR } from '../api/client';
import { ErrorState } from './ErrorState';
import { Loading } from './Loading';
import { Table } from './Table';
import { formatDuration, formatTimeAgo, TimeAgo } from './TimeAgo';

const now = new Date('2026-09-28T09:10:00Z');

describe('formatTimeAgo', () => {
  it.each([
    ['2026-09-28T09:09:50Z', 'just now'],
    ['2026-09-28T09:09:00Z', '1 minute ago'],
    ['2026-09-28T08:40:00Z', '30 minutes ago'],
    ['2026-09-28T07:10:00Z', '2 hours ago'],
    ['2026-09-27T09:10:00Z', 'yesterday'],
    ['2026-09-21T14:12:03Z', '7 days ago'],
    ['2026-09-07T09:10:00Z', '3 weeks ago'],
    ['2026-06-02T12:45:30Z', '4 months ago'],
    ['2024-09-28T09:10:00Z', '2 years ago'],
    ['2026-09-28T09:15:00Z', 'in 5 minutes'],
  ])('%s is "%s"', (iso, want) => {
    expect(formatTimeAgo(new Date(iso), now)).toBe(want);
  });
});

describe('formatDuration', () => {
  it.each([
    [0, '0s'],
    [4_400, '4s'],
    [147_000, '2m 27s'],
    [3_723_000, '1h 2m'],
    [-5_000, '0s'],
  ])('%i ms is %s', (ms, want) => {
    expect(formatDuration(ms)).toBe(want);
  });
});

describe('TimeAgo', () => {
  it('renders a time element with the absolute time as title', () => {
    render(<TimeAgo value="2026-09-28T08:40:00Z" now={now} />);
    const el = screen.getByText('30 minutes ago');
    expect(el.tagName).toBe('TIME');
    expect(el).toHaveAttribute('datetime', '2026-09-28T08:40:00Z');
    expect(el).toHaveAttribute('title', '2026-09-28 08:40:00 UTC');
  });

  it('renders a dash without a value and the raw text when unparseable', () => {
    const { container } = render(
      <>
        <TimeAgo value={undefined} />
        <TimeAgo value="soon" />
      </>,
    );
    expect(container.textContent).toBe('—soon');
  });
});

describe('Table', () => {
  const rows = [
    { id: 'a', name: 'alpha', n: 1 },
    { id: 'b', name: 'beta', n: 22 },
  ];

  it('renders headers, rows and a caption', () => {
    render(
      <Table
        caption="Things"
        rows={rows}
        rowKey={(r) => r.id}
        rowClass={(r) => (r.n > 10 ? 'row--warning' : undefined)}
        columns={[
          { key: 'name', header: 'Name', render: (r) => r.name },
          { key: 'n', header: 'Count', numeric: true, render: (r) => r.n },
        ]}
      />,
    );
    const table = screen.getByRole('table', { name: 'Things' });
    expect(table).toBeInTheDocument();
    expect(screen.getAllByRole('columnheader').map((h) => h.textContent)).toEqual(['Name', 'Count']);
    expect(screen.getAllByRole('row')).toHaveLength(3);
    expect(screen.getByText('22')).toHaveClass('num');
    expect(screen.getByText('beta').closest('tr')).toHaveClass('row--warning');
  });

  it('renders the empty state instead of an empty table', () => {
    render(<Table caption="Things" rows={[]} rowKey={() => ''} columns={[]} empty="Nothing here." />);
    expect(screen.queryByRole('table')).not.toBeInTheDocument();
    expect(screen.getByText('Nothing here.')).toBeInTheDocument();
  });
});

describe('ErrorState', () => {
  it('offers sign-in on 401', () => {
    render(<ErrorState error={new ApiError(401, 'unauthorized', 'no session')} />);
    expect(screen.getByRole('alert')).toHaveTextContent('Signed out');
    expect(screen.getByRole('link', { name: 'Sign in with GitHub' })).toHaveAttribute('href', '/auth/login');
  });

  it('explains a 404 for the named resource', () => {
    render(<ErrorState error={new ApiError(404, 'not_found', 'run not found')} what="run" />);
    expect(screen.getByRole('heading', { name: 'Not found' })).toBeInTheDocument();
    expect(screen.getByText('This run does not exist or you do not have access to it.')).toBeInTheDocument();
  });

  it('retries other failures', () => {
    const retry = vi.fn();
    render(<ErrorState error={new ApiError(0, NETWORK_ERROR, 'GET /v1/me: Failed to fetch')} onRetry={retry} />);
    expect(screen.getByRole('heading', { name: 'The server is unreachable' })).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Try again' }));
    expect(retry).toHaveBeenCalledTimes(1);
  });

  it('shows plain errors', () => {
    render(<ErrorState error={new Error('boom')} />);
    expect(screen.getByRole('heading', { name: 'Something went wrong' })).toBeInTheDocument();
    expect(screen.getByText('boom')).toBeInTheDocument();
    expect(screen.queryByRole('button')).not.toBeInTheDocument();
  });
});

describe('Loading', () => {
  it('is a polite status', () => {
    render(<Loading label="Loading graph…" />);
    expect(screen.getByRole('status')).toHaveTextContent('Loading graph…');
  });
});
