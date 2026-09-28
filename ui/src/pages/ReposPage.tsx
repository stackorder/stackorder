import { useApi } from '../api/context';
import { useResource } from '../api/useResource';
import { ErrorState } from '../components/ErrorState';
import { Loading } from '../components/Loading';
import { Table } from '../components/Table';
import { TimeAgo } from '../components/TimeAgo';
import { repoPath } from '../format';
import { usePageTitle } from '../usePageTitle';

/** Every repository the signed-in user can see. */
export function ReposPage() {
  usePageTitle('Repositories');
  const api = useApi();
  const res = useResource((signal) => api.repos({ signal }), []);
  if (res.error && !res.data) return <ErrorState error={res.error} onRetry={res.reload} />;
  if (!res.data) return <Loading />;
  const items = res.data.items ?? [];
  return (
    <div class="page">
      <h1>Repositories</h1>
      <Table
        caption="Repositories"
        rows={items}
        rowKey={(r) => String(r.id)}
        empty="No repositories yet. Install the GitHub App on a repository to see it here."
        columns={[
          { key: 'name', header: 'Repository', render: (r) => <a href={repoPath(r.full_name)}>{r.full_name}</a> },
          { key: 'branch', header: 'Default branch', render: (r) => <code>{r.default_branch}</code> },
          { key: 'stacks', header: 'Stacks', numeric: true, render: (r) => r.stacks },
          {
            key: 'drifted',
            header: 'Drifted',
            numeric: true,
            render: (r) => (r.drifted > 0 ? <span class="text-warning">{r.drifted}</span> : 0),
          },
          { key: 'locks', header: 'Locks held', numeric: true, render: (r) => r.locks_held },
          { key: 'last', header: 'Last run', render: (r) => <TimeAgo value={r.last_run_at} /> },
        ]}
      />
    </div>
  );
}
