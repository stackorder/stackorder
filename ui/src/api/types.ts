// Mirror of api/v1/types.go: non-omitempty slices and maps are `| null` because Go encodes nil as null.

/** Distinguishes the two node types of the dependency graph. */
export type NodeKind = 'stack' | 'module';

/** Where a module's source lives. */
export type ModuleKind = 'local' | 'git' | 'registry';

/** One of the three edge kinds of the dependency graph. */
export type EdgeType = 'depends_on' | 'uses_module' | 'reads_state';

/** The Terraform-compatible binary a stack is run with. */
export type Tool = 'terraform' | 'tofu';

/** What a run does to its stacks. */
export type RunMode = 'plan' | 'apply' | 'drift';

/** What started a run. */
export type Trigger = 'pull_request' | 'comment' | 'push' | 'schedule' | 'rerequest' | 'manual';

/** The state of a run as a whole. */
export type RunStatus =
  | 'pending'
  | 'planning'
  | 'planned'
  | 'applying'
  | 'applied'
  | 'failed'
  | 'unconfirmed'
  | 'superseded';

/** The state of one stack inside a run. */
export type StackStatus =
  | 'pending'
  | 'planning'
  | 'planned'
  | 'applying'
  | 'applied'
  | 'failed'
  | 'blocked'
  | 'noop'
  | 'unconfirmed'
  | 'unknown'
  | 'skipped';

/** What the CLI reports for one plan, apply or drift execution. */
export type ResultStatus = 'success' | 'failure' | 'error';

/** The verdict of a named policy or cost check. */
export type CheckStatus = 'pass' | 'fail' | 'warn';

/** Why a stack is in the affected set. */
export type Reason = 'changed' | 'module' | 'dependent' | 'requested' | 'reads_state';

/** Every run status in state machine order. */
export const RUN_STATUSES: readonly RunStatus[] = [
  'pending',
  'planning',
  'planned',
  'applying',
  'applied',
  'failed',
  'unconfirmed',
  'superseded',
];

/** Every stack status in state machine order. */
export const STACK_STATUSES: readonly StackStatus[] = [
  'pending',
  'planning',
  'planned',
  'applying',
  'applied',
  'failed',
  'blocked',
  'noop',
  'unconfirmed',
  'unknown',
  'skipped',
];

/** Mirrors RunStatus.Terminal: no further transitions are expected. */
export function runTerminal(status: RunStatus): boolean {
  return status === 'applied' || status === 'failed' || status === 'unconfirmed' || status === 'superseded';
}

/** Mirrors StackStatus.Terminal: the stack has finished for this run. */
export function stackTerminal(status: StackStatus): boolean {
  return !(status === 'pending' || status === 'planning' || status === 'planned' || status === 'applying');
}

/** Mirrors v1.QualifiedStackKey. */
export function qualifiedStackKey(repo: string, key: string): string {
  return `${repo}//${key}`;
}

/** Mirrors v1.SplitQualifiedStackKey; repo is empty for an unqualified key. */
export function splitQualifiedStackKey(qualified: string): { repo: string; key: string } {
  const i = qualified.indexOf('//');
  if (i < 0) return { repo: '', key: qualified };
  return { repo: qualified.slice(0, i), key: qualified.slice(i + 2) };
}

/** Where a stack keeps its state. */
export interface Backend {
  type: string;
  bucket?: string;
  key?: string;
  region?: string;
  dynamodb_table?: string;
  use_lockfile?: boolean;
  workspace_key_prefix?: string;
}

/** Narrows the apply policy for one stack. */
export interface StackApplyConfig {
  allowed_teams?: string[];
}

/** A per-stack .stackorder.yaml. */
export interface StackConfig {
  depends_on?: string[];
  workspace?: string;
  tool?: Tool;
  tool_version?: string;
  environment?: string;
  apply?: StackApplyConfig;
  plan_output?: string;
  ignore_inferred?: string[];
}

/** A node of the graph that Terraform runs in. */
export interface Stack {
  key: string;
  path: string;
  workspace?: string;
  repo?: string;
  backend?: Backend;
  environment?: string;
  tool?: Tool;
  tool_version?: string;
  plan_output?: string;
  config?: StackConfig;
  external?: boolean;
}

/** A node of the graph that stacks and other modules consume. */
export interface Module {
  key: string;
  kind: ModuleKind;
  path?: string;
  source: string;
  ref?: string;
}

/** Points at a node by kind and key. */
export interface NodeRef {
  kind: NodeKind;
  key: string;
}

/** Connects two nodes. */
export interface Edge {
  from: NodeRef;
  to: NodeRef;
  type: EdgeType;
  inferred?: boolean;
  meta?: Record<string, string>;
}

/** The dependency graph of one repository at one commit. */
export interface Graph {
  repo: string;
  sha: string;
  tree_hash?: string;
  stacks: Stack[] | null;
  modules: Module[] | null;
  edges: Edge[] | null;
  warnings?: string[];
}

/** An orchestration lock. */
export interface LockInfo {
  stack_id?: string;
  stack_key?: string;
  run_id: string;
  pr_number?: number;
  taken_at: string;
  reason?: string;
}

/** One entry of a resolution result. */
export interface AffectedStack {
  key: string;
  path: string;
  workspace?: string;
  wave: number;
  reasons: Reason[] | null;
  environment?: string;
  tool?: Tool;
  tool_version?: string;
  plan_output?: string;
  via?: string[];
  locked_by?: LockInfo;
}

/** One element of the GitHub Actions matrix include list. */
export interface MatrixEntry {
  stack: string;
  key: string;
  workspace: string;
  environment: string;
  wave: number;
  tool: Tool;
  tool_version: string;
  plan_output: string;
  sha?: string;
  plan_run_id?: number;
  artifact?: string;
}

/** The JSON GitHub Actions expects in a strategy.matrix expression. */
export interface Matrix {
  include: MatrixEntry[] | null;
}

/** The redacted, size independent digest of a plan. */
export interface PlanSummary {
  adds: number;
  changes: number;
  destroys: number;
  replaces: number;
  imports?: number;
  moves?: number;
  added?: string[];
  changed?: string[];
  destroyed?: string[];
  replaced?: string[];
  output_changes?: number;
}

/** A stored check verdict. */
export interface Check {
  name: string;
  status: CheckStatus;
  summary?: string;
  details_url?: string;
  updated_at: string;
}

/** One stack's row inside a run. */
export interface RunStack {
  stack_id: string;
  key: string;
  path: string;
  workspace?: string;
  environment?: string;
  wave: number;
  status: StackStatus;
  reasons?: Reason[];
  summary?: PlanSummary;
  exit_code?: number;
  job_url?: string;
  plan_artifact?: string;
  plan_text?: string;
  truncated?: boolean;
  checks?: Check[];
  blocked_by?: string[];
  plan_output?: string;
  plan_url?: string;
  started_at?: string;
  finished_at?: string;
}

/** The human and automation view of a run. */
export interface Run {
  id: string;
  repo: string;
  sha: string;
  base_sha?: string;
  pr_number?: number;
  trigger: Trigger;
  mode: RunMode;
  status: RunStatus;
  requested_by?: string;
  created_at: string;
  started_at?: string;
  finished_at?: string;
  waves: number;
  current_wave: number;
  stacks?: RunStack[];
  warnings?: string[];
  html_url?: string;
}

/** The body returned by POST /v1/runs and, for a re-run, POST /v1/runs/{id}/rerun. */
export interface CreateRunResponse {
  run_id: string;
  status: RunStatus;
  existing: boolean;
  run?: Run;
}

/** Points at one stack row in one run. */
export interface RunStackRef {
  run_id: string;
  sha: string;
  pr_number?: number;
  status: StackStatus;
  summary?: PlanSummary;
  finished_at?: string;
  job_url?: string;
}

/** The latest drift observation for a stack. */
export interface DriftStatus {
  checked_at: string;
  drifted: boolean;
  summary?: PlanSummary;
  issue_number?: number;
  issue_url?: string;
}

/** Records that a stack pins a module at a ref. */
export interface ModuleConsume {
  module_key: string;
  ref?: string;
  latest?: string;
  behind?: number;
}

/** The body of GET /v1/stacks/{id}. */
export interface StackDetail {
  id: string;
  repo: string;
  key: string;
  path: string;
  workspace?: string;
  environment?: string;
  backend?: Backend;
  tool?: Tool;
  last_apply?: RunStackRef;
  last_plan?: RunStackRef;
  drift?: DriftStatus;
  lock?: LockInfo;
  depends_on?: string[];
  dependents?: string[];
  modules?: ModuleConsume[];
}

/** One released version of a git module. */
export interface ModuleVersion {
  version: string;
  sha?: string;
  tagged_at: string;
}

/** A stack that uses a module. */
export interface ModuleConsumer {
  stack_id: string;
  repo: string;
  stack_key: string;
  ref?: string;
  behind?: number;
}

/** The body of GET /v1/modules/{id}. */
export interface ModuleDetail {
  id: string;
  key: string;
  kind: ModuleKind;
  source: string;
  versions?: ModuleVersion[];
  consumers?: ModuleConsumer[];
}

/** The body of GET /v1/repos/{owner}/{repo}/graph. */
export interface GraphView {
  repo: string;
  sha: string;
  graph: Graph;
  stack_ids?: Record<string, string>;
  affected?: AffectedStack[];
  waves?: string[][];
}

/** The body of GET /v1/overview. */
export interface Overview {
  repos: number;
  stacks: number;
  drifted: number;
  locks_held: number;
  runs_by_status: Partial<Record<RunStatus, number>> | null;
  stacks_by_status: Partial<Record<StackStatus, number>> | null;
  recent_runs?: Run[];
}

/** One row of GET /v1/repos. */
export interface RepoSummary {
  id: number;
  full_name: string;
  default_branch: string;
  stacks: number;
  drifted: number;
  locks_held: number;
  last_run_at?: string;
}

/** The body of POST /v1/stacks/{id}/unlock. */
export interface UnlockRequest {
  repo?: string;
  stack_key?: string;
  reason?: string;
  force_state?: boolean;
}

/** Returned by the unlock endpoints. */
export interface UnlockResponse {
  released: LockInfo[] | null;
}

/** One row of GET /v1/audit. */
export interface AuditEntry {
  at: string;
  actor: string;
  action: string;
  target?: string;
  details?: Record<string, unknown>;
}

/** The body of every non-2xx response (v1.Error). */
export interface ApiErrorBody {
  code: string;
  message: string;
  details?: unknown;
}

/** Wraps list responses. */
export interface Page<T> {
  items: T[] | null;
  next_cursor?: string;
  total?: number;
}

/** The body of GET /v1/me. */
export interface Whoami {
  login: string;
  avatar_url?: string;
  orgs: string[] | null;
  admin?: boolean;
}
