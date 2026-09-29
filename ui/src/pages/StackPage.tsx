import { useState } from 'preact/hooks';

import { useApi } from '../api/context';
import { splitQualifiedStackKey, type RunStackRef, type StackDetail, type UnlockResponse } from '../api/types';
import { useResource } from '../api/useResource';
import { ErrorState } from '../components/ErrorState';
import { Loading } from '../components/Loading';
import { RunLink } from '../components/RunLink';
import { Badge, StatusBadge } from '../components/StatusBadge';
import { SummaryCounts } from '../components/SummaryCounts';
import { Table } from '../components/Table';
import { TimeAgo } from '../components/TimeAgo';
import { UnlockDialog } from '../components/UnlockDialog';
import {
  commitUrl,
  githubOrigin,
  issueUrl,
  moduleBaseKey,
  plural,
  pullUrl,
  repoPath,
  s3ConsoleUrl,
  safeUrl,
  shortId,
  shortSha,
  splitRepo,
  stateUri,
} from '../format';
import { usePageTitle } from '../usePageTitle';

interface Links {
  repo: string;
  origin: string;
  stackHref: (key: string) => string;
}

function RefCard({ title, stackRef, links, empty }: { title: string; stackRef?: RunStackRef; links: Links; empty: string }) {
  const id = `card-${title.toLowerCase().replace(/\W+/g, '-')}`;
  const jobUrl = safeUrl(stackRef?.job_url);
  return (
    <section class="card" aria-labelledby={id}>
      <h2 id={id}>{title}</h2>
      {!stackRef ? (
        <p class="empty">{empty}</p>
      ) : (
        <>
          <p class="card__headline">
            <StatusBadge status={stackRef.status} /> <SummaryCounts summary={stackRef.summary} />
          </p>
          <dl class="kv">
            <dt>Run</dt>
            <dd>
              <RunLink id={stackRef.run_id} />
            </dd>
            <dt>Commit</dt>
            <dd>
              <a class="mono" href={commitUrl(links.repo, stackRef.sha, links.origin)}>
                {shortSha(stackRef.sha)}
              </a>
            </dd>
            {stackRef.pr_number ? (
              <>
                <dt>Pull request</dt>
                <dd>
                  <a href={pullUrl(links.repo, stackRef.pr_number, links.origin)}>#{stackRef.pr_number}</a>
                </dd>
              </>
            ) : null}
            <dt>Finished</dt>
            <dd>
              <TimeAgo value={stackRef.finished_at} />
            </dd>
          </dl>
          {jobUrl && (
            <a class="card__link" href={jobUrl}>
              Job log
            </a>
          )}
        </>
      )}
    </section>
  );
}

function DriftCard({ stack, links }: { stack: StackDetail; links: Links }) {
  const drift = stack.drift;
  const issueHref =
    safeUrl(drift?.issue_url) ?? (drift?.issue_number ? issueUrl(links.repo, drift.issue_number, links.origin) : undefined);
  return (
    <section class="card" aria-labelledby="card-drift">
      <h2 id="card-drift">Drift</h2>
      {!drift ? (
        <p class="empty">No drift check has run yet.</p>
      ) : (
        <>
          <p class="card__headline">
            {drift.drifted ? <Badge tone="warning">drifted</Badge> : <Badge tone="success">in sync</Badge>}{' '}
            {drift.drifted && <SummaryCounts summary={drift.summary} />}
          </p>
          <dl class="kv">
            <dt>Checked</dt>
            <dd>
              <TimeAgo value={drift.checked_at} />
            </dd>
            {issueHref ? (
              <>
                <dt>Issue</dt>
                <dd>
                  <a href={issueHref}>
                    {drift.issue_number ? `#${String(drift.issue_number)}` : 'Drift issue'}
                  </a>
                </dd>
              </>
            ) : null}
          </dl>
        </>
      )}
    </section>
  );
}

function LockCard({ stack, links, onUnlock, flash }: { stack: StackDetail; links: Links; onUnlock: () => void; flash?: string }) {
  const lock = stack.lock;
  return (
    <section class="card" aria-labelledby="card-lock">
      <h2 id="card-lock">Lock</h2>
      {flash && (
        <p class="flash" role="status">
          {flash}
        </p>
      )}
      {!lock ? (
        <p class="empty">Not locked.</p>
      ) : (
        <>
          <p class="card__headline">
            <Badge tone="running">locked</Badge>{' '}
            {lock.pr_number ? (
              <>
                by <a href={pullUrl(links.repo, lock.pr_number, links.origin)}>PR #{lock.pr_number}</a>
              </>
            ) : (
              'by a manual run'
            )}
          </p>
          <dl class="kv">
            <dt>Run</dt>
            <dd>
              <RunLink id={lock.run_id} />
            </dd>
            <dt>Taken</dt>
            <dd>
              <TimeAgo value={lock.taken_at} />
            </dd>
            {lock.reason && (
              <>
                <dt>Reason</dt>
                <dd>{lock.reason}</dd>
              </>
            )}
          </dl>
          <button type="button" class="button button--danger" onClick={onUnlock}>
            Unlock…
          </button>
        </>
      )}
    </section>
  );
}

function StackList({ title, keys, links, empty }: { title: string; keys: string[] | undefined; links: Links; empty: string }) {
  const id = `list-${title.toLowerCase().replace(/\W+/g, '-')}`;
  return (
    <section class="card" aria-labelledby={id}>
      <h2 id={id}>{title}</h2>
      {!keys?.length ? (
        <p class="empty">{empty}</p>
      ) : (
        <ul class="plain-list">
          {keys.map((key) => (
            <li key={key}>
              <a href={links.stackHref(key)}>{key}</a>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}

function unlockedMessage(res: UnlockResponse | undefined): string {
  if (!res) return 'Lock released.';
  const released = res.released ?? [];
  if (released.length === 0) return 'The lock was already released.';
  const first = released[0];
  const from = first?.pr_number ? `PR #${String(first.pr_number)}` : first ? `run ${shortId(first.run_id)}` : '';
  return `Released ${plural(released.length, 'lock')}${from ? ` held by ${from}` : ''}.`;
}

/** One stack: its latest apply, plan and drift, its lock, neighbours, modules and history. */
export function StackPage({ id }: { id: string }) {
  return <StackView key={id} id={id} />;
}

function StackView({ id }: { id: string }) {
  const api = useApi();
  const stack = useResource((signal) => api.stack(id, { signal }), [id]);
  const history = useResource((signal) => api.stackRuns(id, { signal }), [id]);
  const repoName = stack.data?.repo ?? '';
  const siblings = useResource(
    (signal) => {
      if (!repoName) return Promise.resolve(undefined);
      const { owner, name } = splitRepo(repoName);
      return api.repoStacks(owner, name, { signal });
    },
    [repoName],
  );
  const [unlocking, setUnlocking] = useState(false);
  const [flash, setFlash] = useState<string>();
  usePageTitle(stack.data?.key ?? 'Stack');

  if (stack.error && !stack.data) return <ErrorState error={stack.error} onRetry={stack.reload} what="stack" />;
  if (!stack.data) return <Loading />;
  const s = stack.data;

  const ids = new Map((siblings.data?.items ?? []).map((st) => [st.key, st.id]));
  const links: Links = {
    repo: s.repo,
    origin: githubOrigin([s.last_plan?.job_url, s.last_apply?.job_url, s.drift?.issue_url].find((u) => safeUrl(u))),
    stackHref: (key) => {
      const q = splitQualifiedStackKey(key);
      if (q.repo && q.repo !== s.repo) return repoPath(q.repo);
      const stackId = ids.get(q.key);
      return stackId ? `/stacks/${encodeURIComponent(stackId)}` : repoPath(s.repo);
    },
  };
  const consoleUrl = s3ConsoleUrl(s.backend, s.workspace);

  return (
    <div class="page">
      <div class="page-header">
        <p class="breadcrumb">
          <a href={repoPath(s.repo)}>{s.repo}</a>
        </p>
        <h1>{s.key}</h1>
        <dl class="meta">
          <div>
            <dt>Environment</dt>
            <dd>{s.environment ?? 'default'}</dd>
          </div>
          {s.instance && (
            <div>
              <dt>Instance</dt>
              <dd>{s.instance}</dd>
            </div>
          )}
          {s.workspace && (
            <div>
              <dt>Workspace</dt>
              <dd>{s.workspace}</dd>
            </div>
          )}
          <div>
            <dt>Tool</dt>
            <dd>{s.tool ?? 'terraform'}</dd>
          </div>
          {s.backend && (
            <div>
              <dt>State</dt>
              <dd>
                {consoleUrl ? (
                  <a href={consoleUrl} class="mono" rel="noreferrer">
                    {stateUri(s.backend, s.workspace)}
                  </a>
                ) : (
                  <span class="mono">{s.backend.type}</span>
                )}
              </dd>
            </div>
          )}
        </dl>
      </div>

      <div class="grid grid--4">
        <RefCard title="Last apply" stackRef={s.last_apply} links={links} empty="Never applied through Stackorder." />
        <RefCard title="Last plan" stackRef={s.last_plan} links={links} empty="No plan recorded yet." />
        <DriftCard stack={s} links={links} />
        <LockCard
          stack={s}
          links={links}
          flash={flash}
          onUnlock={() => {
            setFlash(undefined);
            setUnlocking(true);
          }}
        />
      </div>

      <div class="grid grid--2">
        <StackList title="Depends on" keys={s.depends_on} links={links} empty="No dependencies." />
        <StackList title="Depended on by" keys={s.dependents} links={links} empty="Nothing depends on this stack." />
      </div>

      <section aria-labelledby="stack-modules">
        <h2 id="stack-modules">Modules</h2>
        <Table
          caption={`Modules used by ${s.key}`}
          rows={s.modules ?? []}
          rowKey={(m) => m.module_key}
          empty="This stack uses no modules."
          columns={[
            {
              key: 'module',
              header: 'Module',
              render: (m) => <a href={`/modules?q=${encodeURIComponent(moduleBaseKey(m.module_key))}`}>{m.module_key}</a>,
            },
            { key: 'ref', header: 'Pinned', render: (m) => (m.ref ? <code>{m.ref}</code> : <span class="muted">local</span>) },
            { key: 'latest', header: 'Latest', render: (m) => (m.latest ? <code>{m.latest}</code> : <span class="muted">—</span>) },
            {
              key: 'behind',
              header: 'Behind',
              render: (m) =>
                m.behind ? (
                  <Badge tone="warning">{plural(m.behind, 'version')} behind</Badge>
                ) : m.latest ? (
                  <Badge tone="success">up to date</Badge>
                ) : (
                  <span class="muted">—</span>
                ),
            },
          ]}
        />
      </section>

      <section aria-labelledby="stack-history">
        <h2 id="stack-history">History</h2>
        {history.error && !history.data ? (
          <ErrorState error={history.error} onRetry={history.reload} />
        ) : !history.data ? (
          <Loading label="Loading history…" />
        ) : (
          <Table
            caption={`Run history of ${s.key}`}
            rows={history.data.items ?? []}
            rowKey={(r) => r.run_id}
            empty="No runs have touched this stack yet."
            columns={[
              { key: 'run', header: 'Run', render: (r) => <RunLink id={r.run_id} /> },
              {
                key: 'sha',
                header: 'Commit',
                render: (r) => (
                  <a class="mono" href={commitUrl(s.repo, r.sha, links.origin)}>
                    {shortSha(r.sha)}
                  </a>
                ),
              },
              {
                key: 'pr',
                header: 'PR',
                render: (r) =>
                  r.pr_number ? <a href={pullUrl(s.repo, r.pr_number, links.origin)}>#{r.pr_number}</a> : <span class="muted">—</span>,
              },
              { key: 'status', header: 'Status', render: (r) => <StatusBadge status={r.status} /> },
              { key: 'changes', header: 'Changes', render: (r) => <SummaryCounts summary={r.summary} /> },
              { key: 'finished', header: 'Finished', render: (r) => <TimeAgo value={r.finished_at} /> },
              {
                key: 'job',
                header: 'Job',
                render: (r) => {
                  const jobUrl = safeUrl(r.job_url);
                  return jobUrl ? <a href={jobUrl}>log</a> : <span class="muted">—</span>;
                },
              },
            ]}
          />
        )}
      </section>

      <UnlockDialog
        open={unlocking}
        stackId={s.id}
        stackKey={s.key}
        onClose={() => {
          setUnlocking(false);
        }}
        onUnlocked={(res) => {
          setUnlocking(false);
          setFlash(unlockedMessage(res));
          stack.reload();
          history.reload();
        }}
      />
    </div>
  );
}
