import { useLocation } from 'preact-iso';
import { useEffect, useState } from 'preact/hooks';

import { ApiError } from '../api/client';
import { useApi } from '../api/context';
import { runTerminal, type PlanSummary, type Run, type RunStack } from '../api/types';
import { useResource } from '../api/useResource';
import { Dialog } from '../components/Dialog';
import { ErrorState, LOGIN_PATH } from '../components/ErrorState';
import { Loading } from '../components/Loading';
import { runTitle } from '../components/RunLink';
import { Badge, StatusBadge, statusTone } from '../components/StatusBadge';
import { SummaryCounts } from '../components/SummaryCounts';
import { Table } from '../components/Table';
import { formatDuration, TimeAgo } from '../components/TimeAgo';
import { commitUrl, githubOrigin, humanize, pullUrl, repoPath, safeUrl, shortId, shortSha } from '../format';
import { usePageTitle } from '../usePageTitle';

/** How often a run that is still in flight is refreshed. */
export const REFRESH_MS = 10_000;

/** One wave of a run and its stacks. */
export interface RunWave {
  wave: number;
  stacks: RunStack[];
}

/** Groups a run's stacks into its waves, keeping empty waves the run declares. */
export function runWaves(run: Pick<Run, 'waves' | 'stacks'>): RunWave[] {
  const stacks = run.stacks ?? [];
  const count = Math.max(run.waves, ...stacks.map((s) => s.wave + 1), 0);
  const waves: RunWave[] = Array.from({ length: count }, (_, wave) => ({ wave, stacks: [] }));
  for (const s of stacks) waves[s.wave]?.stacks.push(s);
  for (const w of waves) w.stacks.sort((a, b) => a.key.localeCompare(b.key));
  return waves;
}

function AddressList({ summary }: { summary: PlanSummary | undefined }) {
  if (!summary) return null;
  const groups: [string, string[] | undefined][] = [
    ['Added', summary.added],
    ['Changed', summary.changed],
    ['Destroyed', summary.destroyed],
    ['Replaced', summary.replaced],
  ];
  const present = groups.filter(([, list]) => list?.length);
  if (present.length === 0) return null;
  return (
    <section class="drawer__section" aria-labelledby="drawer-addresses">
      <h3 id="drawer-addresses">Resources</h3>
      <dl class="addresses">
        {present.map(([label, list]) => (
          <div key={label}>
            <dt>{label}</dt>
            {list?.map((a) => (
              <dd key={a} class="mono">
                {a}
              </dd>
            ))}
          </div>
        ))}
      </dl>
    </section>
  );
}

interface PlanView {
  text: string | undefined;
  full: boolean;
  loading: boolean;
  error: Error | undefined;
}

function usePlanText(runId: string, stack: RunStack): PlanView {
  const api = useApi();
  const wanted = Boolean(stack.truncated && stack.plan_url);
  const [state, setState] = useState<{ key: string; text?: string; error?: Error }>({ key: '' });
  const key = `${runId} ${stack.key}`;
  useEffect(() => {
    if (!wanted) return;
    const ctrl = new AbortController();
    api.planText(runId, stack.key, { signal: ctrl.signal }).then(
      (text) => {
        if (!ctrl.signal.aborted) setState({ key, text });
      },
      (err: unknown) => {
        if (!ctrl.signal.aborted) setState({ key, error: err instanceof Error ? err : new Error(String(err)) });
      },
    );
    return () => {
      ctrl.abort();
    };
  }, [api, runId, stack.key, key, wanted]);
  const current = state.key === key ? state : { key };
  if (!wanted) return { text: stack.plan_text, full: false, loading: false, error: undefined };
  if (current.text !== undefined) return { text: current.text, full: true, loading: false, error: undefined };
  return { text: stack.plan_text, full: false, loading: current.error === undefined, error: current.error };
}

function PlanOutput({ runId, stack, jobUrl }: { runId: string; stack: RunStack; jobUrl: string | undefined }) {
  const plan = usePlanText(runId, stack);
  const jobLog = jobUrl ? <a href={jobUrl}>The job log has the full plan.</a> : 'The job log has the full plan.';
  if (!plan.text && !plan.loading && !plan.error) {
    return (
      <p class="empty">
        No plan text was recorded. Stacks with <code>plan_output: summary</code> and stacks that did not run only report
        counts.
      </p>
    );
  }
  return (
    <>
      {plan.full && <p class="muted">The full plan, from the server&apos;s artifact bucket.</p>}
      {plan.loading && (
        <p class="muted" role="status">
          Loading the full plan from the server&apos;s artifact bucket…
        </p>
      )}
      {plan.error && (
        <p class="text-warning" role="status">
          {plan.text ? 'Only the beginning of the plan is shown: the' : 'The'} full plan could not be loaded (
          {plan.error.message}). {jobLog}
        </p>
      )}
      {stack.truncated && !stack.plan_url && (
        <p class="text-warning">
          The plan text was truncated at 256 KB. {jobLog}
        </p>
      )}
      {plan.text && (
        <pre class="plan" tabindex={0} aria-label={`Plan output of ${stack.key}`}>
          {plan.text}
        </pre>
      )}
    </>
  );
}

function StackDetails({ runId, stack, onClose }: { runId: string; stack: RunStack; onClose: () => void }) {
  const jobUrl = safeUrl(stack.job_url);
  return (
    <div class="drawer__body">
      <div class="drawer__header">
        <h2 id="drawer-title">{stack.key}</h2>
        <button type="button" class="button button--small" onClick={onClose}>
          Close
        </button>
      </div>
      <p>
        <StatusBadge status={stack.status} /> <SummaryCounts summary={stack.summary} />
      </p>
      <dl class="kv">
        <dt>Wave</dt>
        <dd>{stack.wave}</dd>
        <dt>Environment</dt>
        <dd>{stack.environment ?? 'default'}</dd>
        {stack.reasons?.length ? (
          <>
            <dt>Affected because</dt>
            <dd>{stack.reasons.map(humanize).join(', ')}</dd>
          </>
        ) : null}
        {stack.exit_code !== undefined && (
          <>
            <dt>Exit code</dt>
            <dd class="mono">{stack.exit_code}</dd>
          </>
        )}
        {stack.started_at && (
          <>
            <dt>Started</dt>
            <dd>
              <TimeAgo value={stack.started_at} />
            </dd>
          </>
        )}
        {stack.started_at && stack.finished_at && (
          <>
            <dt>Duration</dt>
            <dd>{formatDuration(new Date(stack.finished_at).getTime() - new Date(stack.started_at).getTime())}</dd>
          </>
        )}
        {stack.plan_artifact && (
          <>
            <dt>Plan artifact</dt>
            <dd class="mono">{stack.plan_artifact}</dd>
          </>
        )}
      </dl>
      {jobUrl && (
        <p>
          <a href={jobUrl}>Open the job log</a>
        </p>
      )}
      <AddressList summary={stack.summary} />
      {stack.checks?.length ? (
        <section class="drawer__section" aria-labelledby="drawer-checks">
          <h3 id="drawer-checks">Checks</h3>
          <Table
            caption={`Checks on ${stack.key}`}
            rows={stack.checks}
            rowKey={(c) => c.name}
            columns={[
              { key: 'name', header: 'Check', render: (c) => c.name },
              { key: 'status', header: 'Verdict', render: (c) => <StatusBadge status={c.status} /> },
              {
                key: 'summary',
                header: 'Summary',
                render: (c) => {
                  const href = safeUrl(c.details_url);
                  return href ? <a href={href}>{c.summary ?? 'details'}</a> : c.summary;
                },
              },
            ]}
          />
        </section>
      ) : null}
      <section class="drawer__section" aria-labelledby="drawer-plan">
        <h3 id="drawer-plan">Plan output</h3>
        <PlanOutput runId={runId} stack={stack} jobUrl={jobUrl} />
      </section>
    </div>
  );
}

function StackCard({ stack, onDetails }: { stack: RunStack; onDetails: () => void }) {
  const jobUrl = safeUrl(stack.job_url);
  return (
    <article class={`stack-card tone-border-${statusTone(stack.status)}`} aria-labelledby={`stack-${stack.stack_id}`}>
      <h3 id={`stack-${stack.stack_id}`} class="stack-card__title">
        <a href={`/stacks/${encodeURIComponent(stack.stack_id)}`}>{stack.key}</a>
      </h3>
      <p class="stack-card__status">
        <StatusBadge status={stack.status} /> <SummaryCounts summary={stack.summary} />
      </p>
      <p class="stack-card__meta">
        {stack.environment ?? 'default'}
        {stack.reasons?.length ? ` · ${stack.reasons.map(humanize).join(', ')}` : ''}
      </p>
      <div class="stack-card__actions">
        {jobUrl && <a href={jobUrl}>Job log</a>}
        <button
          type="button"
          class="button button--small"
          aria-haspopup="dialog"
          aria-label={`Details of ${stack.key}`}
          onClick={onDetails}
        >
          Details
        </button>
      </div>
    </article>
  );
}

/** One run: its status, waves with per-stack results, plan details, and re-run. */
export function RunPage({ id }: { id: string }) {
  return <RunView key={id} id={id} />;
}

function RunView({ id }: { id: string }) {
  const api = useApi();
  const { route } = useLocation();
  const res = useResource((signal) => api.run(id, { signal }), [id]);
  const [selected, setSelected] = useState<string>();
  const [rerun, setRerun] = useState<{ busy: boolean; error?: string; message?: string }>({ busy: false });
  const run = res.data;
  usePageTitle(run ? `Run ${shortId(run.id)}` : 'Run');

  const refreshError = res.error instanceof ApiError ? res.error : undefined;
  const signedOut = refreshError?.unauthorized === true;
  const refused = signedOut || refreshError?.notFound === true || refreshError?.status === 403;
  const polling = run !== undefined && !runTerminal(run.status) && !refused;
  const { reload } = res;
  useEffect(() => {
    if (!polling) return;
    const timer = setInterval(reload, REFRESH_MS);
    return () => {
      clearInterval(timer);
    };
  }, [polling, reload]);

  if (res.error && !run) return <ErrorState error={res.error} onRetry={res.reload} what="run" />;
  if (!run) return <Loading />;

  const origin = githubOrigin(run.stacks?.find((s) => safeUrl(s.job_url))?.job_url);
  const waves = runWaves(run);
  const selectedStack = run.stacks?.find((s) => s.key === selected);

  const startRerun = async () => {
    setRerun({ busy: true });
    try {
      const created = await api.rerun(run.id);
      if (created?.run_id && created.run_id !== run.id) {
        route(`/runs/${encodeURIComponent(created.run_id)}`);
        setRerun({ busy: false });
      } else {
        setRerun({ busy: false, message: 'Re-run requested.' });
        res.reload();
      }
    } catch (err) {
      setRerun({ busy: false, error: err instanceof Error ? err.message : String(err) });
    }
  };

  return (
    <div class="page">
      <div class="page-header">
        <p class="breadcrumb">
          <a href={repoPath(run.repo)}>{run.repo}</a>
        </p>
        <h1>
          Run <span class="mono">{shortId(run.id)}</span> <StatusBadge status={run.status} class="badge--large" />
        </h1>
        <p class="page-header__meta">{runTitle(run)}</p>
        <dl class="meta">
          {run.pr_number ? (
            <div>
              <dt>Pull request</dt>
              <dd>
                <a href={pullUrl(run.repo, run.pr_number, origin)}>#{run.pr_number}</a>
              </dd>
            </div>
          ) : null}
          <div>
            <dt>Commit</dt>
            <dd>
              <a class="mono" href={commitUrl(run.repo, run.sha, origin)}>
                {shortSha(run.sha)}
              </a>
              {run.base_sha && (
                <span class="muted">
                  {' '}
                  from <span class="mono">{shortSha(run.base_sha)}</span>
                </span>
              )}
            </dd>
          </div>
          <div>
            <dt>Trigger</dt>
            <dd>{humanize(run.trigger)}</dd>
          </div>
          {run.requested_by && (
            <div>
              <dt>Requested by</dt>
              <dd>{run.requested_by}</dd>
            </div>
          )}
          <div>
            <dt>Started</dt>
            <dd>
              <TimeAgo value={run.started_at ?? run.created_at} />
            </dd>
          </div>
          {run.finished_at && (
            <div>
              <dt>Duration</dt>
              <dd>
                {formatDuration(new Date(run.finished_at).getTime() - new Date(run.started_at ?? run.created_at).getTime())}
              </dd>
            </div>
          )}
        </dl>
        <div class="actions">
          <a class="button" href={repoPath(run.repo, { run: run.id })}>
            View in graph
          </a>
          <button type="button" class="button button--primary" onClick={() => void startRerun()} disabled={rerun.busy}>
            {rerun.busy ? 'Re-running…' : 'Re-run'}
          </button>
          {polling && <span class="muted">Refreshing every {REFRESH_MS / 1000} s</span>}
        </div>
        {rerun.error && (
          <p class="form-error" role="alert">
            Re-run failed: {rerun.error}
          </p>
        )}
        {rerun.message && (
          <p class="flash" role="status">
            {rerun.message}
          </p>
        )}
        {res.error && (
          <p class="text-warning" role="status">
            Refresh failed: {res.error.message}
            {signedOut && (
              <>
                {' '}
                <a href={LOGIN_PATH}>Sign in again</a>
              </>
            )}
          </p>
        )}
      </div>

      {run.warnings?.length ? (
        <ul class="warnings" aria-label="Run warnings">
          {run.warnings.map((w) => (
            <li key={w}>{w}</li>
          ))}
        </ul>
      ) : null}

      <div class="waves">
        {waves.map((w) => (
          <section key={w.wave} class="wave" aria-labelledby={`wave-${String(w.wave)}`}>
            <h2 id={`wave-${String(w.wave)}`} class="wave__title">
              Wave {w.wave}
              {polling && w.wave === run.current_wave && <Badge tone="running">current</Badge>}
            </h2>
            {w.stacks.length === 0 ? (
              <p class="empty">No stacks.</p>
            ) : (
              <ul class="wave__stacks">
                {w.stacks.map((s) => (
                  <li key={s.key}>
                    <StackCard
                      stack={s}
                      onDetails={() => {
                        setSelected(s.key);
                      }}
                    />
                  </li>
                ))}
              </ul>
            )}
          </section>
        ))}
      </div>

      <Dialog
        open={selectedStack !== undefined}
        onClose={() => {
          setSelected(undefined);
        }}
        labelledBy="drawer-title"
        class="drawer"
      >
        {selectedStack && (
          <StackDetails
            runId={run.id}
            stack={selectedStack}
            onClose={() => {
              setSelected(undefined);
            }}
          />
        )}
      </Dialog>
    </div>
  );
}
