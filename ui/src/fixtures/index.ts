import type {
  GraphView,
  ModuleDetail,
  Overview,
  Page,
  RepoSummary,
  Run,
  RunStackRef,
  StackDetail,
  Whoami,
} from '../api/types';
import graphRunJson from './graph-run.json' with { type: 'json' };
import graphJson from './graph.json' with { type: 'json' };
import meJson from './me.json' with { type: 'json' };
import moduleJson from './module.json' with { type: 'json' };
import modulesJson from './modules.json' with { type: 'json' };
import overviewJson from './overview.json' with { type: 'json' };
import repoStacksJson from './repo-stacks.json' with { type: 'json' };
import reposJson from './repos.json' with { type: 'json' };
import runJson from './run.json' with { type: 'json' };
import runsJson from './runs.json' with { type: 'json' };
import stackRunsJson from './stack-runs.json' with { type: 'json' };
import stackJson from './stack.json' with { type: 'json' };

/** Deep copies a fixture so a test can mutate it freely. */
export function clone<T>(value: T): T {
  return structuredClone(value);
}

/** GET /v1/me: the signed-in user. */
export const me = meJson as Whoami;
/** GET /v1/overview. */
export const overview = overviewJson as Overview;
/** GET /v1/repos. */
export const repos = reposJson as Page<RepoSummary>;
/** GET /v1/repos/acme/infra/graph: the design's example graph plus a git module pinned by modules/eks. */
export const graph = graphJson as GraphView;
/** GET /v1/repos/acme/infra/graph?run=: the example graph replaying a change to modules/vpc in three waves. */
export const graphRun = graphRunJson as GraphView;
/** GET /v1/repos/acme/infra/runs. */
export const runs = runsJson as Page<Run>;
/** GET /v1/runs/{id}: the PR #42 apply where stacks/prod/eks failed in wave 1 and stacks/prod/apps is blocked. */
export const run = runJson as Run;
/** GET /v1/stacks/{id} for stacks/prod/eks, locked by the failed apply and drifted. */
export const stack = stackJson as StackDetail;
/** GET /v1/stacks/{id}/runs for stacks/prod/eks. */
export const stackRuns = stackRunsJson as Page<RunStackRef>;
/** GET /v1/repos/acme/infra/stacks. */
export const repoStacks = repoStacksJson as Page<StackDetail>;
/** GET /v1/modules. */
export const modules = modulesJson as Page<ModuleDetail>;
/** GET /v1/modules/{id} for the eks-addons git module. */
export const moduleDetail = moduleJson as ModuleDetail;

const planRunSummary = runs.items?.find((r) => r.mode === 'plan' && r.status === 'planned');
if (!planRunSummary) throw new Error('fixtures: runs.json has no planned plan run');

/** The PR #42 plan run that preceded the failed apply, with every stack planned. */
export const planRun: Run = {
  ...planRunSummary,
  stacks: (run.stacks ?? []).map((s) => ({
    ...s,
    status: 'planned',
    exit_code: 0,
    checks: s.checks ?? [],
    started_at: '2026-09-28T08:32:02Z',
    finished_at: '2026-09-28T08:38:12Z',
  })),
};

const truncatedStack = run.stacks?.find((s) => s.truncated && s.plan_url);
if (!truncatedStack?.plan_text) throw new Error('fixtures: run.json has no truncated stack with a plan_url');

/** The stack of the run whose plan text was cut, with the full text in the artifact bucket. */
export const truncatedPlanStack = truncatedStack.key;

/** GET /v1/runs/{id}/stacks/{key}/plan for truncatedPlanStack: the full plan text. */
export const fullPlanText = `${truncatedStack.plan_text}\nPlan: 0 to add, 1 to change, 0 to destroy.\n`;

/** Identifiers the fixtures share, for building URLs in tests. */
export const ids = {
  repo: 'acme/infra',
  run: run.id,
  planRun: planRunSummary.id,
  stack: stack.id,
  module: moduleDetail.id,
  stacks: graph.stack_ids ?? {},
};

/** A canned API response keyed by method and path, without the query string; a string body is served as text/plain. */
export interface FixtureRoute {
  method: 'GET' | 'POST';
  pattern: RegExp;
  body: (url: URL) => unknown;
}

/** The API as the fixtures describe it; unknown stacks, runs and modules are 404. */
export const routes: FixtureRoute[] = [
  { method: 'GET', pattern: /^\/v1\/me$/, body: () => me },
  { method: 'GET', pattern: /^\/v1\/overview$/, body: () => overview },
  { method: 'GET', pattern: /^\/v1\/repos$/, body: () => repos },
  {
    method: 'GET',
    pattern: /^\/v1\/repos\/acme\/infra\/graph$/,
    body: (url) => (url.searchParams.get('run') ? graphRun : graph),
  },
  { method: 'GET', pattern: /^\/v1\/repos\/acme\/infra\/runs$/, body: () => runs },
  { method: 'GET', pattern: /^\/v1\/repos\/acme\/infra\/stacks$/, body: () => repoStacks },
  { method: 'GET', pattern: new RegExp(`^/v1/stacks/${stack.id}$`), body: () => stack },
  { method: 'GET', pattern: new RegExp(`^/v1/stacks/${stack.id}/runs$`), body: () => stackRuns },
  { method: 'GET', pattern: /^\/v1\/modules$/, body: () => modules },
  { method: 'GET', pattern: new RegExp(`^/v1/modules/${moduleDetail.id}$`), body: () => moduleDetail },
  { method: 'GET', pattern: new RegExp(`^/v1/runs/${run.id}$`), body: () => run },
  { method: 'GET', pattern: new RegExp(`^/v1/runs/${planRun.id}$`), body: () => planRun },
  {
    method: 'GET',
    pattern: new RegExp(`^/v1/runs/${run.id}/stacks/${encodeURIComponent(truncatedPlanStack)}/plan$`),
    body: () => fullPlanText,
  },
];

/** Finds the canned body for a request, or undefined for a 404. */
export function lookup(method: string, url: URL): unknown {
  const route = routes.find((r) => r.method === method && r.pattern.test(url.pathname));
  return route?.body(url);
}
