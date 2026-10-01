import type { Run } from '../api/types';
import { plural, repoPath } from '../format';
import { RunLink, runTitle } from './RunLink';
import { StatusBadge } from './StatusBadge';
import { SummaryCounts } from './SummaryCounts';
import { Table, type Column } from './Table';
import { formatDuration, TimeAgo } from './TimeAgo';

const SHOWN_STACK_KEYS = 2;

const repoColumn: Column<Run> = {
  key: 'repo',
  header: 'Repository',
  render: (r) => <a href={repoPath(r.repo)}>{r.repo}</a>,
};

/** The first stack keys of a run, with how many more it covers and all listed keys as the tooltip. */
export function RunStacks({ run }: { run: Pick<Run, 'stack_count' | 'stack_keys'> }) {
  const count = run.stack_count ?? 0;
  const keys = run.stack_keys ?? [];
  if (count === 0) return <span class="muted">—</span>;
  const shown = keys.slice(0, SHOWN_STACK_KEYS);
  const more = count - shown.length;
  const title = keys.length < count ? `${keys.join('\n')}\n…` : keys.join('\n');
  return (
    <span class="run-stacks" title={title}>
      {shown.map((k, i) => (
        <span key={k}>
          {i > 0 && ', '}
          <span class="mono">{k}</span>
        </span>
      ))}
      {more > 0 && <span class="muted">{shown.length > 0 ? ` +${String(more)} more` : plural(more, 'stack')}</span>}
    </span>
  );
}

function RunStatus({ run }: { run: Run }) {
  return (
    <>
      <StatusBadge status={run.status} />
      {run.status === 'applying' && run.waves > 1 && (
        <span class="muted">
          {' '}
          wave {run.current_wave} of {run.waves}
        </span>
      )}
    </>
  );
}

function RunDuration({ run }: { run: Run }) {
  if (!run.finished_at) return <span class="muted">—</span>;
  const ms = new Date(run.finished_at).getTime() - new Date(run.started_at ?? run.created_at).getTime();
  return <>{formatDuration(ms)}</>;
}

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
        { key: 'stacks', header: 'Stacks', render: (r) => <RunStacks run={r} /> },
        { key: 'changes', header: 'Changes', render: (r) => <SummaryCounts summary={r.summary} /> },
        { key: 'status', header: 'Status', render: (r) => <RunStatus run={r} /> },
        {
          key: 'by',
          header: 'Requested by',
          render: (r) => r.requested_by ?? <span class="muted">{r.trigger === 'schedule' ? 'scheduler' : '—'}</span>,
        },
        { key: 'created', header: 'Started', render: (r) => <TimeAgo value={r.started_at ?? r.created_at} /> },
        { key: 'duration', header: 'Duration', render: (r) => <RunDuration run={r} /> },
      ]}
    />
  );
}
