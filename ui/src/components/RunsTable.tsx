import type { Run } from '../api/types';
import { repoPath } from '../format';
import { RunLink, runTitle } from './RunLink';
import { StatusBadge } from './StatusBadge';
import { Table, type Column } from './Table';
import { TimeAgo } from './TimeAgo';

const repoColumn: Column<Run> = {
  key: 'repo',
  header: 'Repository',
  render: (r) => <a href={repoPath(r.repo)}>{r.repo}</a>,
};

/** The recent runs table, shared by the overview and the repository page. */
export function RunsTable({ runs, caption, showRepo = true }: { runs: Run[]; caption: string; showRepo?: boolean }) {
  return (
    <Table
      caption={caption}
      rows={runs}
      rowKey={(r) => r.id}
      empty="No runs yet."
      columns={[
        { key: 'id', header: 'Run', render: (r) => <RunLink id={r.id} /> },
        ...(showRepo ? [repoColumn] : []),
        { key: 'what', header: 'What', render: (r) => runTitle(r) },
        { key: 'status', header: 'Status', render: (r) => <StatusBadge status={r.status} /> },
        {
          key: 'by',
          header: 'Requested by',
          render: (r) => r.requested_by ?? <span class="muted">{r.trigger === 'schedule' ? 'scheduler' : '—'}</span>,
        },
        { key: 'created', header: 'Started', render: (r) => <TimeAgo value={r.started_at ?? r.created_at} /> },
      ]}
    />
  );
}
