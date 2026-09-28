import type {
  ApiErrorBody,
  CreateRunResponse,
  GraphView,
  ModuleDetail,
  Overview,
  Page,
  RepoSummary,
  Run,
  RunStackRef,
  StackDetail,
  UnlockRequest,
  UnlockResponse,
  Whoami,
} from './types';

/** The fetch signature the client needs; tests inject a fake. */
export type FetchLike = (input: string, init?: RequestInit) => Promise<Response>;

/** Error code used when the request never reached the server. */
export const NETWORK_ERROR = 'network';

/** Error code used when a response body is not the JSON the client expected. */
export const INVALID_RESPONSE = 'invalid_response';

/** A failed API call, carrying the HTTP status and the v1.Error code. */
export class ApiError extends Error {
  readonly status: number;
  readonly code: string;
  readonly details: unknown;

  constructor(status: number, code: string, message: string, options?: { details?: unknown; cause?: unknown }) {
    super(message, options?.cause === undefined ? undefined : { cause: options.cause });
    this.name = 'ApiError';
    this.status = status;
    this.code = code;
    this.details = options?.details;
  }

  /** True when the caller must sign in again. */
  get unauthorized(): boolean {
    return this.status === 401;
  }

  /** True when the resource does not exist. */
  get notFound(): boolean {
    return this.status === 404;
  }
}

/** Options for a graph request: a git ref to read and a run to replay. */
export interface GraphQuery {
  ref?: string;
  run?: string;
}

/** Options accepted by every request method. */
export interface RequestOptions {
  signal?: AbortSignal;
}

/** Options for constructing an ApiClient. */
export interface ApiClientOptions {
  fetch?: FetchLike;
  baseUrl?: string;
}

function isErrorBody(value: unknown): value is ApiErrorBody {
  return (
    typeof value === 'object' &&
    value !== null &&
    typeof (value as Record<string, unknown>).code === 'string' &&
    typeof (value as Record<string, unknown>).message === 'string'
  );
}

/** The v1.Error code for an HTTP status, used when the body is not a v1.Error. */
export function codeForStatus(status: number): string {
  switch (status) {
    case 400:
    case 422:
      return 'invalid';
    case 401:
      return 'unauthorized';
    case 403:
      return 'forbidden';
    case 404:
      return 'not_found';
    case 409:
      return 'conflict';
    default:
      return 'internal';
  }
}

function segment(value: string): string {
  return encodeURIComponent(value);
}

function isAbort(err: unknown): boolean {
  return err instanceof DOMException && err.name === 'AbortError';
}

/** Typed client for the human endpoints of the Stackorder JSON API. */
export class ApiClient {
  private readonly fetchImpl: FetchLike;
  private readonly baseUrl: string;

  constructor(options: ApiClientOptions = {}) {
    this.fetchImpl = options.fetch ?? ((input, init) => globalThis.fetch(input, init));
    this.baseUrl = (options.baseUrl ?? '').replace(/\/+$/, '');
  }

  /** GET /v1/me: the signed-in user. */
  me(opts?: RequestOptions): Promise<Whoami> {
    return this.get('/v1/me', opts);
  }

  /** GET /v1/overview: org-wide counts and recent runs. */
  overview(opts?: RequestOptions): Promise<Overview> {
    return this.get('/v1/overview', opts);
  }

  /** GET /v1/repos: repositories the user can see. */
  repos(opts?: RequestOptions): Promise<Page<RepoSummary>> {
    return this.get('/v1/repos', opts);
  }

  /** GET /v1/repos/{owner}/{repo}/graph: the graph at a ref, optionally replaying a run. */
  repoGraph(owner: string, repo: string, query: GraphQuery = {}, opts?: RequestOptions): Promise<GraphView> {
    const params = new URLSearchParams();
    if (query.ref) params.set('ref', query.ref);
    if (query.run) params.set('run', query.run);
    const qs = params.toString();
    return this.get(`/v1/repos/${segment(owner)}/${segment(repo)}/graph${qs ? `?${qs}` : ''}`, opts);
  }

  /** GET /v1/repos/{owner}/{repo}/runs: the repository's runs, newest first. */
  repoRuns(owner: string, repo: string, opts?: RequestOptions): Promise<Page<Run>> {
    return this.get(`/v1/repos/${segment(owner)}/${segment(repo)}/runs`, opts);
  }

  /** GET /v1/repos/{owner}/{repo}/stacks: the repository's stacks with their ids. */
  repoStacks(owner: string, repo: string, opts?: RequestOptions): Promise<Page<StackDetail>> {
    return this.get(`/v1/repos/${segment(owner)}/${segment(repo)}/stacks`, opts);
  }

  /** GET /v1/stacks/{id}: stack detail. */
  stack(id: string, opts?: RequestOptions): Promise<StackDetail> {
    return this.get(`/v1/stacks/${segment(id)}`, opts);
  }

  /** GET /v1/stacks/{id}/runs: the stack's history, newest first. */
  stackRuns(id: string, opts?: RequestOptions): Promise<Page<RunStackRef>> {
    return this.get(`/v1/stacks/${segment(id)}/runs`, opts);
  }

  /** GET /v1/modules: every module the server tracks. */
  modules(opts?: RequestOptions): Promise<Page<ModuleDetail>> {
    return this.get('/v1/modules', opts);
  }

  /** GET /v1/modules/{id}: module versions and consumers. */
  module(id: string, opts?: RequestOptions): Promise<ModuleDetail> {
    return this.get(`/v1/modules/${segment(id)}`, opts);
  }

  /** GET /v1/runs/{id}: run detail with per-stack rows. */
  run(id: string, opts?: RequestOptions): Promise<Run> {
    return this.get(`/v1/runs/${segment(id)}`, opts);
  }

  /** POST /v1/stacks/{id}/unlock: release the stack's orchestration lock. */
  unlockStack(id: string, body: UnlockRequest, opts?: RequestOptions): Promise<UnlockResponse> {
    return this.send('POST', `/v1/stacks/${segment(id)}/unlock`, body, opts);
  }

  /** POST /v1/runs/{id}/rerun: start the run again. */
  rerun(id: string, opts?: RequestOptions): Promise<CreateRunResponse> {
    return this.send('POST', `/v1/runs/${segment(id)}/rerun`, {}, opts);
  }

  /** POST /auth/logout: end the session. */
  async logout(opts?: RequestOptions): Promise<void> {
    await this.request('POST', '/auth/logout', undefined, opts);
  }

  private get<T>(path: string, opts?: RequestOptions): Promise<T> {
    return this.send('GET', path, undefined, opts);
  }

  private async send<T>(method: string, path: string, body: unknown, opts?: RequestOptions): Promise<T> {
    const res = await this.request(method, path, body, opts);
    if (res.status === 204) return undefined as T;
    try {
      return (await res.json()) as T;
    } catch (err) {
      if (isAbort(err)) throw err;
      throw new ApiError(res.status, INVALID_RESPONSE, `${method} ${path}: response is not JSON`, { cause: err });
    }
  }

  private async request(method: string, path: string, body: unknown, opts?: RequestOptions): Promise<Response> {
    const headers: Record<string, string> = { Accept: 'application/json' };
    const init: RequestInit = { method, headers, credentials: 'same-origin' };
    if (body !== undefined) {
      headers['Content-Type'] = 'application/json';
      init.body = JSON.stringify(body);
    }
    if (opts?.signal) init.signal = opts.signal;

    let res: Response;
    try {
      res = await this.fetchImpl(this.baseUrl + path, init);
    } catch (err) {
      if (isAbort(err)) throw err;
      const message = err instanceof Error ? err.message : String(err);
      throw new ApiError(0, NETWORK_ERROR, `${method} ${path}: ${message}`, { cause: err });
    }
    if (res.ok) return res;

    let parsed: unknown;
    try {
      parsed = await res.json();
    } catch {
      parsed = undefined;
    }
    if (isErrorBody(parsed)) {
      throw new ApiError(res.status, parsed.code, parsed.message, { details: parsed.details });
    }
    const text = res.statusText || `HTTP ${res.status}`;
    throw new ApiError(res.status, codeForStatus(res.status), `${method} ${path}: ${text}`);
  }
}
