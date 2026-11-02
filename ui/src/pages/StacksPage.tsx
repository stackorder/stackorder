import { useLocation } from 'preact-iso';

import { useApi } from '../api/context';
import type { StackDetail } from '../api/types';
import { useResource } from '../api/useResource';
import { ErrorState } from '../components/ErrorState';
import { Loading } from '../components/Loading';
import { RunLink } from '../components/RunLink';
import { Badge, StatusBadge } from '../components/StatusBadge';
import { Table } from '../components/Table';
import { TimeAgo } from '../components/TimeAgo';
import { repoPath } from '../format';
import { usePageTitle } from '../usePageTitle';

/** Whether a stack matches a free-text filter on its key, repository, path or environment. */
export function matchesStack(s: Pick<StackDetail, 'key' | 'repo' | 'path' | 'environment'>, filter: string): boolean {
  const q = filter.trim().toLowerCase();
  if (!q) return true;
  return [s.key, s.repo, s.path, s.environment ?? 'default'].some((v) => v.toLowerCase().includes(q));
}

function LastApply({ stack }: { stack: StackDetail }) {
  const apply = stack.last_apply;
  if (!apply) return <span class="muted">—</span>;
  return (
    <span class="nowrap">
      <StatusBadge status={apply.status} /> <RunLink id={apply.run_id} />
      {apply.finished_at && (
        <>
          {' '}
          <TimeAgo value={apply.finished_at} />
        </>
      )}
    </span>
  );
}

/** Every stack of every repository the signed-in user can see. */
export function StacksPage() {
  usePageTitle('Stacks');
  const api = useApi();
  const { query, route } = useLocation();
  const filter = query.q ?? '';
  const res = useResource((signal) => api.stacks({ signal }), []);
  if (res.error && !res.data) return <ErrorState error={res.error} onRetry={res.reload} />;
  if (!res.data) return <Loading />;
  const all = res.data.items ?? [];
  const rows = all.filter((s) => matchesStack(s, filter));
  return (
    <div class="page">
      <h1>Stacks</h1>
      <form
        class="controls"
        role="search"
        onSubmit={(e) => {
          e.preventDefault();
        }}
      >
        <label for="stack-filter">Filter</label>
        <input
          id="stack-filter"
          type="search"
          value={filter}
          placeholder="stack, repository, path or environment"
          spellcheck={false}
          onInput={(e) => {
            const q = e.currentTarget.value;
            route(q ? `/stacks?q=${encodeURIComponent(q)}` : '/stacks', true);
          }}
        />
        <span class="muted" aria-live="polite">
          {rows.length} of {all.length}
        </span>
      </form>
      <Table
        caption="Stacks"
        rows={rows}
        rowKey={(s) => s.id}
        empty={filter ? 'No stack matches the filter.' : 'No stacks yet. They appear here once Stackorder has scanned a repository.'}
        columns={[
          { key: 'key', header: 'Stack', render: (s) => <a href={`/stacks/${encodeURIComponent(s.id)}`}>{s.key}</a> },
          { key: 'repo', header: 'Repository', render: (s) => <a href={repoPath(s.repo)}>{s.repo}</a> },
          { key: 'environment', header: 'Environment', render: (s) => s.environment ?? 'default' },
          { key: 'apply', header: 'Last apply', render: (s) => <LastApply stack={s} /> },
          {
            key: 'drift',
            header: 'Drift',
            render: (s) =>
              !s.drift ? (
                <span class="muted">—</span>
              ) : s.drift.drifted ? (
                <Badge tone="warning">drifted</Badge>
              ) : (
                <Badge tone="success">in sync</Badge>
              ),
          },
          {
            key: 'lock',
            header: 'Lock',
            render: (s) => (s.lock ? <Badge tone="running">locked</Badge> : <span class="muted">—</span>),
          },
        ]}
      />
    </div>
  );
}
