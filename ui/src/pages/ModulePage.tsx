import { useApi } from '../api/context';
import { useResource } from '../api/useResource';
import { ErrorState } from '../components/ErrorState';
import { Loading } from '../components/Loading';
import { Badge } from '../components/StatusBadge';
import { Table } from '../components/Table';
import { TimeAgo } from '../components/TimeAgo';
import { plural, repoPath, shortSha } from '../format';
import { usePageTitle } from '../usePageTitle';
import { latestVersion } from './ModulesPage';

/** One module: its released versions and every stack that consumes it at which ref. */
export function ModulePage({ id }: { id: string }) {
  const api = useApi();
  const res = useResource((signal) => api.module(id, { signal }), [id]);
  usePageTitle(res.data?.key ?? 'Module');
  if (res.error && !res.data) return <ErrorState error={res.error} onRetry={res.reload} what="module" />;
  if (!res.data) return <Loading />;
  const m = res.data;
  const latest = latestVersion(m);
  const versions = [...(m.versions ?? [])].sort((a, b) => b.tagged_at.localeCompare(a.tagged_at));
  return (
    <div class="page">
      <div class="page-header">
        <p class="breadcrumb">
          <a href="/modules">Modules</a>
        </p>
        <h1 class="wrap">{m.key}</h1>
        <dl class="meta">
          <div>
            <dt>Kind</dt>
            <dd>{m.kind}</dd>
          </div>
          <div>
            <dt>Source</dt>
            <dd>
              <code class="wrap">{m.source}</code>
            </dd>
          </div>
          {latest && (
            <div>
              <dt>Latest</dt>
              <dd>
                <code>{latest}</code>
              </dd>
            </div>
          )}
        </dl>
      </div>

      <section aria-labelledby="module-versions">
        <h2 id="module-versions">Versions</h2>
        <Table
          caption={`Versions of ${m.key}`}
          rows={versions}
          rowKey={(v) => v.version}
          empty={
            m.kind === 'git'
              ? 'No tags recorded yet. Versions appear when the module repository, with the App installed, pushes a semver tag.'
              : `${m.kind === 'local' ? 'Local' : 'Registry'} modules have no tracked versions.`
          }
          columns={[
            { key: 'version', header: 'Version', render: (v) => <code>{v.version}</code> },
            { key: 'sha', header: 'Commit', render: (v) => (v.sha ? <span class="mono">{shortSha(v.sha)}</span> : '—') },
            { key: 'tagged', header: 'Tagged', render: (v) => <TimeAgo value={v.tagged_at} /> },
          ]}
        />
      </section>

      <section aria-labelledby="module-consumers">
        <h2 id="module-consumers">Consumers</h2>
        <Table
          caption={`Stacks that use ${m.key}`}
          rows={m.consumers ?? []}
          rowKey={(c) => `${c.repo}//${c.stack_key}`}
          empty="No stack uses this module."
          rowClass={(c) => ((c.behind ?? 0) > 0 ? 'row--warning' : undefined)}
          columns={[
            {
              key: 'stack',
              header: 'Stack',
              render: (c) => <a href={`/stacks/${encodeURIComponent(c.stack_id)}`}>{c.stack_key}</a>,
            },
            { key: 'repo', header: 'Repository', render: (c) => <a href={repoPath(c.repo)}>{c.repo}</a> },
            { key: 'ref', header: 'Pinned', render: (c) => (c.ref ? <code>{c.ref}</code> : <span class="muted">local</span>) },
            {
              key: 'behind',
              header: 'Behind',
              render: (c) =>
                c.behind ? (
                  <Badge tone="warning">{plural(c.behind, 'version')} behind</Badge>
                ) : c.ref && latest ? (
                  <Badge tone="success">up to date</Badge>
                ) : (
                  <span class="muted">—</span>
                ),
            },
          ]}
        />
      </section>
    </div>
  );
}
