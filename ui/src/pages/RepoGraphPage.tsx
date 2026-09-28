import { useLocation } from 'preact-iso';
import { useMemo } from 'preact/hooks';

import { useApi } from '../api/context';
import type { Edge, GraphView, Run } from '../api/types';
import { useResource } from '../api/useResource';
import { ErrorState } from '../components/ErrorState';
import { Graph, GraphLegend } from '../components/Graph';
import { Loading } from '../components/Loading';
import { RunLink, runTitle } from '../components/RunLink';
import { RunsTable } from '../components/RunsTable';
import { StatusBadge } from '../components/StatusBadge';
import { Table } from '../components/Table';
import { formatTimeAgo } from '../components/TimeAgo';
import { humanize, moduleBaseKey, plural, repoPath, shortId, shortSha } from '../format';
import { layoutGraph, type GraphLayout, type LayoutNode } from '../graph/layout';
import { usePageTitle } from '../usePageTitle';

function runOption(r: Run, now: Date): string {
  return `${shortId(r.id)} · ${runTitle(r)} · ${humanize(r.status)} · ${formatTimeAgo(new Date(r.created_at), now)}`;
}

function edgeRows(view: GraphView): (Edge & { id: string })[] {
  return (view.graph.edges ?? []).map((e, i) => ({ ...e, id: String(i) }));
}

function Replay({ run, layout, runError }: { run: Run | undefined; layout: GraphLayout; runError: Error | undefined }) {
  const affected = layout.waves.reduce((n, w) => n + w.stacks.length, 0);
  return (
    <section class="card replay" aria-labelledby="replay-title">
      <h2 id="replay-title">Affected set</h2>
      {run ? (
        <p class="replay__run">
          <StatusBadge status={run.status} /> <RunLink id={run.id} /> {runTitle(run)} at <code>{shortSha(run.sha)}</code>
        </p>
      ) : (
        runError && <p class="text-warning">Stack statuses are unavailable: {runError.message}</p>
      )}
      <p>
        {plural(affected, 'affected stack')} in {plural(layout.waves.length, 'wave')}.
      </p>
      <ol class="wave-list">
        {layout.waves.map((w) => (
          <li key={w.wave}>
            <h3>Wave {w.wave}</h3>
            <ul>
              {w.stacks.map((s) => (
                <li key={s.key}>
                  {s.stackId ? <a href={`/stacks/${encodeURIComponent(s.stackId)}`}>{s.key}</a> : s.key}{' '}
                  {s.status && <StatusBadge status={s.status} />}
                </li>
              ))}
            </ul>
          </li>
        ))}
      </ol>
    </section>
  );
}

/** A repository's dependency graph, with any run's affected set replayed on it. */
export function RepoGraphPage({ owner, repo }: { owner: string; repo: string }) {
  const fullName = `${owner}/${repo}`;
  usePageTitle(fullName);
  const api = useApi();
  const { query, route } = useLocation();
  const ref = query.ref ?? '';
  const runId = query.run ?? '';

  const graph = useResource(
    (signal) => api.repoGraph(owner, repo, { ref, run: runId }, { signal }),
    [owner, repo, ref, runId],
  );
  const runs = useResource((signal) => api.repoRuns(owner, repo, { signal }), [owner, repo]);
  const run = useResource(
    (signal) => (runId ? api.run(runId, { signal }) : Promise.resolve(undefined)),
    [runId],
  );
  const layout = useMemo(
    () => (graph.data ? layoutGraph(graph.data, run.data) : undefined),
    [graph.data, run.data],
  );

  const navigate = (next: { ref?: string; run?: string }) => {
    route(repoPath(fullName, { ref: next.ref ?? ref, run: next.run ?? runId }));
  };

  const activate = (node: LayoutNode) => {
    if (node.kind === 'module') {
      route(`/modules?q=${encodeURIComponent(moduleBaseKey(node.key))}`);
    } else if (node.stackId) {
      route(`/stacks/${encodeURIComponent(node.stackId)}`);
    } else if (node.repo) {
      route(repoPath(node.repo));
    }
  };

  const now = new Date();
  const runItems = runs.data?.items ?? [];
  const options = runItems.map((r) => ({ id: r.id, label: runOption(r, now) }));
  if (runId && !runItems.some((r) => r.id === runId)) options.unshift({ id: runId, label: shortId(runId) });

  return (
    <div class="page">
      <div class="page-header">
        <h1>{fullName}</h1>
        {graph.data && (
          <p class="page-header__meta">
            Graph at <code title={graph.data.sha}>{shortSha(graph.data.sha)}</code>
          </p>
        )}
      </div>
      <div class="controls">
        <form
          class="controls__ref"
          onSubmit={(e) => {
            e.preventDefault();
            const value = new FormData(e.currentTarget).get('ref');
            navigate({ ref: typeof value === 'string' ? value.trim() : '' });
          }}
        >
          <label for="graph-ref">Ref</label>
          <input id="graph-ref" name="ref" type="text" defaultValue={ref} placeholder="default branch" spellcheck={false} />
          <button type="submit" class="button">
            Show
          </button>
        </form>
        <div class="controls__run">
          <label for="graph-run">Replay run</label>
          <select
            id="graph-run"
            value={runId}
            onChange={(e) => {
              navigate({ run: e.currentTarget.value });
            }}
          >
            <option value="">None: current graph</option>
            {options.map((o) => (
              <option key={o.id} value={o.id}>
                {o.label}
              </option>
            ))}
          </select>
          {runs.error && <span class="text-warning">Runs are unavailable: {runs.error.message}</span>}
        </div>
      </div>
      {graph.error && !graph.data ? (
        <ErrorState error={graph.error} onRetry={graph.reload} what="repository" />
      ) : !graph.data || !layout ? (
        <Loading label="Loading graph…" />
      ) : (
        <>
          {graph.data.graph.warnings?.length ? (
            <ul class="warnings" aria-label="Graph warnings">
              {graph.data.graph.warnings.map((w) => (
                <li key={w}>{w}</li>
              ))}
            </ul>
          ) : null}
          <div class="graph-layout">
            <Graph layout={layout} label={`Dependency graph of ${fullName}`} onActivate={activate} />
            <aside class="graph-side">
              {layout.replay && <Replay run={run.data} layout={layout} runError={run.error} />}
              <GraphLegend replay={layout.replay} />
            </aside>
          </div>
          <details class="card">
            <summary>
              All edges as a table ({(graph.data.graph.edges ?? []).length})
            </summary>
            <Table
              caption={`Edges of ${fullName}`}
              rows={edgeRows(graph.data)}
              rowKey={(e) => e.id}
              columns={[
                { key: 'from', header: 'From', render: (e) => <code>{e.from.key}</code> },
                {
                  key: 'type',
                  header: 'Edge',
                  render: (e) => `${humanize(e.type)}${e.inferred ? ' (inferred)' : ''}${e.meta?.ref ? ` @ ${e.meta.ref}` : ''}`,
                },
                { key: 'to', header: 'To', render: (e) => <code>{e.to.key}</code> },
              ]}
            />
          </details>
        </>
      )}
      <section aria-labelledby="repo-runs">
        <h2 id="repo-runs">Runs</h2>
        {runs.data ? (
          <RunsTable runs={runItems} caption={`Runs of ${fullName}`} showRepo={false} />
        ) : runs.error ? null : (
          <Loading label="Loading runs…" />
        )}
      </section>
    </div>
  );
}
