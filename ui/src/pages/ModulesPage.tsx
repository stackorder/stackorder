import { useLocation } from 'preact-iso';

import { useApi } from '../api/context';
import type { ModuleDetail } from '../api/types';
import { useResource } from '../api/useResource';
import { ErrorState } from '../components/ErrorState';
import { Loading } from '../components/Loading';
import { Badge } from '../components/StatusBadge';
import { Table } from '../components/Table';
import { moduleBaseKey } from '../format';
import { usePageTitle } from '../usePageTitle';

/** Number of consumers pinned behind the latest version. */
export function consumersBehind(m: Pick<ModuleDetail, 'consumers'>): number {
  return (m.consumers ?? []).filter((c) => (c.behind ?? 0) > 0).length;
}

/** Whether a module matches a free-text filter on its key or source. */
export function matchesModule(m: Pick<ModuleDetail, 'key' | 'source'>, filter: string): boolean {
  const q = filter.trim().toLowerCase();
  if (!q) return true;
  return m.key.toLowerCase().includes(q) || m.source.toLowerCase().includes(q) || moduleBaseKey(m.key).toLowerCase() === q;
}

/** Every module the server tracks, with how many consumers lag behind. */
export function ModulesPage() {
  usePageTitle('Modules');
  const api = useApi();
  const { query, route } = useLocation();
  const filter = query.q ?? '';
  const res = useResource((signal) => api.modules({ signal }), []);
  if (res.error && !res.data) return <ErrorState error={res.error} onRetry={res.reload} />;
  if (!res.data) return <Loading />;
  const all = res.data.items ?? [];
  const rows = all.filter((m) => matchesModule(m, filter));
  return (
    <div class="page">
      <h1>Modules</h1>
      <form
        class="controls"
        role="search"
        onSubmit={(e) => {
          e.preventDefault();
        }}
      >
        <label for="module-filter">Filter</label>
        <input
          id="module-filter"
          type="search"
          value={filter}
          placeholder="module key or source"
          spellcheck={false}
          onInput={(e) => {
            const q = e.currentTarget.value;
            route(q ? `/modules?q=${encodeURIComponent(q)}` : '/modules', true);
          }}
        />
        <span class="muted" aria-live="polite">
          {rows.length} of {all.length}
        </span>
      </form>
      <Table
        caption="Modules"
        rows={rows}
        rowKey={(m) => m.id}
        empty={filter ? 'No module matches the filter.' : 'No modules recorded yet.'}
        columns={[
          { key: 'key', header: 'Module', render: (m) => <a href={`/modules/${encodeURIComponent(m.id)}`}>{m.key}</a> },
          { key: 'kind', header: 'Kind', render: (m) => m.kind },
          { key: 'source', header: 'Source', render: (m) => <code class="wrap">{m.source}</code> },
          { key: 'latest', header: 'Latest', render: (m) => m.latest ?? <span class="muted">—</span> },
          { key: 'consumers', header: 'Consumers', numeric: true, render: (m) => (m.consumers ?? []).length },
          {
            key: 'behind',
            header: 'Behind',
            render: (m) => {
              const n = consumersBehind(m);
              return n > 0 ? <Badge tone="warning">{n} behind</Badge> : <span class="muted">—</span>;
            },
          },
        ]}
      />
    </div>
  );
}
