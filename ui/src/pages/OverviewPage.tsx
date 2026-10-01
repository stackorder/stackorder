import { useApi } from '../api/context';
import { RUN_STATUSES, STACK_STATUSES, type RunStatus, type StackStatus } from '../api/types';
import { useResource } from '../api/useResource';
import { ErrorState } from '../components/ErrorState';
import { Loading } from '../components/Loading';
import { RunsTable } from '../components/RunsTable';
import { StatusBadge, statusTone } from '../components/StatusBadge';
import { usePageTitle } from '../usePageTitle';

function StatusBreakdown<S extends RunStatus | StackStatus>({
  title,
  order,
  counts,
}: {
  title: string;
  order: readonly S[];
  counts: Partial<Record<S, number>> | null;
}) {
  const rows = order.map((s) => ({ status: s, count: counts?.[s] ?? 0 })).filter((r) => r.count > 0);
  const total = rows.reduce((sum, r) => sum + r.count, 0);
  const id = `breakdown-${title.toLowerCase().replace(/\W+/g, '-')}`;
  return (
    <section class="card" aria-labelledby={id}>
      <h2 id={id}>{title}</h2>
      {total === 0 ? (
        <p class="empty">None yet.</p>
      ) : (
        <>
          <div class="bar" aria-hidden="true">
            {rows.map((r) => (
              <span
                key={r.status}
                class={`bar__segment tone-${statusTone(r.status)}`}
                style={{ flexGrow: r.count }}
                title={`${r.status}: ${String(r.count)}`}
              />
            ))}
          </div>
          <ul class="breakdown">
            {rows.map((r) => (
              <li key={r.status}>
                <StatusBadge status={r.status} />
                <span class="breakdown__count">{r.count}</span>
              </li>
            ))}
          </ul>
        </>
      )}
    </section>
  );
}

/** Org overview: counts, stacks and runs by status, drift, locks and recent runs. */
export function OverviewPage() {
  usePageTitle('Overview');
  const api = useApi();
  const res = useResource((signal) => api.overview({ signal }), []);
  if (res.error && !res.data) return <ErrorState error={res.error} onRetry={res.reload} />;
  if (!res.data) return <Loading />;
  const o = res.data;
  return (
    <div class="page">
      <h1>Overview</h1>
      <dl class="stats">
        <div class="stat">
          <dt>Repositories</dt>
          <dd>
            <a href="/repos">{o.repos}</a>
          </dd>
        </div>
        <div class="stat">
          <dt>Stacks</dt>
          <dd>{o.stacks}</dd>
        </div>
        <div class={`stat${o.drifted > 0 ? ' stat--warning' : ''}`}>
          <dt>Drifted</dt>
          <dd>{o.drifted}</dd>
        </div>
        <div class={`stat${o.locks_held > 0 ? ' stat--info' : ''}`}>
          <dt>Locks held</dt>
          <dd>{o.locks_held}</dd>
        </div>
      </dl>
      <div class="grid grid--2">
        <StatusBreakdown title="Stacks by status" order={STACK_STATUSES} counts={o.stacks_by_status} />
        <StatusBreakdown title="Runs by status" order={RUN_STATUSES} counts={o.runs_by_status} />
      </div>
      <section aria-labelledby="recent-runs">
        <h2 id="recent-runs">Recent runs</h2>
        <RunsTable runs={o.recent_runs ?? []} caption="Recent runs" showRepo={o.repos > 1} />
      </section>
    </div>
  );
}
