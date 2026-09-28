import { describe, expect, it, vi } from 'vitest';

import { ApiClient, ApiError, INVALID_RESPONSE, NETWORK_ERROR, type FetchLike } from './client';

function respond(body: unknown, status = 200, contentType = 'application/json'): Response {
  const text = typeof body === 'string' ? body : JSON.stringify(body);
  return new Response(status === 204 ? null : text, { status, headers: { 'Content-Type': contentType } });
}

function client(response: Response | (() => Promise<Response>)) {
  const fetch = vi.fn<FetchLike>(() => (typeof response === 'function' ? response() : Promise.resolve(response)));
  return { api: new ApiClient({ fetch }), fetch };
}

function lastCall(fetch: ReturnType<typeof vi.fn<FetchLike>>): { url: string; init: RequestInit } {
  const call = fetch.mock.calls.at(-1);
  if (!call) throw new Error('fetch was not called');
  return { url: call[0], init: call[1] ?? {} };
}

describe('ApiClient requests', () => {
  const id = '7d9f1b3c-5e7a-4b9c-8d1e-3f5a7b9c1d66';
  it.each([
    ['me', (a: ApiClient) => a.me(), '/v1/me'],
    ['overview', (a: ApiClient) => a.overview(), '/v1/overview'],
    ['repos', (a: ApiClient) => a.repos(), '/v1/repos'],
    ['repoGraph', (a: ApiClient) => a.repoGraph('acme', 'infra'), '/v1/repos/acme/infra/graph'],
    ['repoGraph with ref', (a: ApiClient) => a.repoGraph('acme', 'infra', { ref: 'main' }), '/v1/repos/acme/infra/graph?ref=main'],
    [
      'repoGraph with ref and run',
      (a: ApiClient) => a.repoGraph('acme', 'infra', { ref: 'feature/x', run: id }),
      `/v1/repos/acme/infra/graph?ref=feature%2Fx&run=${id}`,
    ],
    ['repoGraph with empty query', (a: ApiClient) => a.repoGraph('acme', 'infra', { ref: '', run: '' }), '/v1/repos/acme/infra/graph'],
    ['repoRuns', (a: ApiClient) => a.repoRuns('acme', 'infra'), '/v1/repos/acme/infra/runs'],
    ['repoStacks', (a: ApiClient) => a.repoStacks('acme', 'acme.github.io'), '/v1/repos/acme/acme.github.io/stacks'],
    ['stack', (a: ApiClient) => a.stack(id), `/v1/stacks/${id}`],
    ['stackRuns', (a: ApiClient) => a.stackRuns(id), `/v1/stacks/${id}/runs`],
    ['modules', (a: ApiClient) => a.modules(), '/v1/modules'],
    ['module', (a: ApiClient) => a.module(id), `/v1/modules/${id}`],
    ['run', (a: ApiClient) => a.run(id), `/v1/runs/${id}`],
    ['encoded segment', (a: ApiClient) => a.run('a/b c'), '/v1/runs/a%2Fb%20c'],
  ])('%s is GET %s', async (_name, call, path) => {
    const { api, fetch } = client(respond({ ok: true }));
    await expect(call(api)).resolves.toEqual({ ok: true });
    const { url, init } = lastCall(fetch);
    expect(url).toBe(path);
    expect(init.method).toBe('GET');
    expect(init.body).toBeUndefined();
    expect(init.credentials).toBe('same-origin');
    expect(init.headers).toEqual({ Accept: 'application/json' });
  });

  it('posts an unlock request as JSON', async () => {
    const { api, fetch } = client(respond({ released: [] }));
    await expect(api.unlockStack(id, { reason: 'runner died' })).resolves.toEqual({ released: [] });
    const { url, init } = lastCall(fetch);
    expect(url).toBe(`/v1/stacks/${id}/unlock`);
    expect(init.method).toBe('POST');
    expect(init.headers).toEqual({ Accept: 'application/json', 'Content-Type': 'application/json' });
    expect(JSON.parse(init.body as string)).toEqual({ reason: 'runner died' });
  });

  it('posts a re-run with an empty body', async () => {
    const { api, fetch } = client(respond({ run_id: 'new', status: 'pending', existing: false }));
    await expect(api.rerun(id)).resolves.toMatchObject({ run_id: 'new' });
    const { url, init } = lastCall(fetch);
    expect(url).toBe(`/v1/runs/${id}/rerun`);
    expect(init.method).toBe('POST');
    expect(init.body).toBe('{}');
  });

  it('logs out without expecting a body', async () => {
    const { api, fetch } = client(respond(null, 204));
    await expect(api.logout()).resolves.toBeUndefined();
    expect(lastCall(fetch).url).toBe('/auth/logout');
    expect(lastCall(fetch).init.method).toBe('POST');
  });

  it('resolves 204 responses to undefined', async () => {
    const { api } = client(respond(null, 204));
    await expect(api.unlockStack(id, {})).resolves.toBeUndefined();
  });

  it('prefixes a base URL without doubling slashes', async () => {
    const fetch = vi.fn<FetchLike>(() => Promise.resolve(respond({})));
    await new ApiClient({ fetch, baseUrl: 'https://stackorder.example.com/' }).me();
    expect(lastCall(fetch).url).toBe('https://stackorder.example.com/v1/me');
  });

  it('passes the abort signal through', async () => {
    const { api, fetch } = client(respond({}));
    const ctrl = new AbortController();
    await api.overview({ signal: ctrl.signal });
    expect(lastCall(fetch).init.signal).toBe(ctrl.signal);
  });

  it('uses the global fetch when none is injected', async () => {
    const spy = vi.spyOn(globalThis, 'fetch').mockResolvedValue(respond({ login: 'octocat', orgs: [] }));
    await expect(new ApiClient().me()).resolves.toEqual({ login: 'octocat', orgs: [] });
    expect(spy).toHaveBeenCalledWith('/v1/me', expect.objectContaining({ method: 'GET' }));
  });
});

describe('ApiClient errors', () => {
  it.each([
    [401, { code: 'unauthorized', message: 'no session' }, 'unauthorized', 'no session'],
    [403, { code: 'forbidden', message: 'write permission required' }, 'forbidden', 'write permission required'],
    [404, { code: 'not_found', message: 'run not found' }, 'not_found', 'run not found'],
    [409, { code: 'locked', message: 'stack is locked by PR #7', details: { pr: 7 } }, 'locked', 'stack is locked by PR #7'],
    [500, 'upstream exploded', 'internal', 'GET /v1/overview: HTTP 500'],
    [502, '<html>bad gateway</html>', 'internal', 'GET /v1/overview: HTTP 502'],
    [400, { error: 'not a v1 error' }, 'invalid', 'GET /v1/overview: HTTP 400'],
    [401, '', 'unauthorized', 'GET /v1/overview: HTTP 401'],
    [404, 'not found', 'not_found', 'GET /v1/overview: HTTP 404'],
    [409, '', 'conflict', 'GET /v1/overview: HTTP 409'],
  ])('maps HTTP %i to an ApiError', async (status, body, code, message) => {
    const { api } = client(respond(body, status, typeof body === 'string' ? 'text/html' : 'application/json'));
    const err: unknown = await api.overview().catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    const apiErr = err as ApiError;
    expect(apiErr.status).toBe(status);
    expect(apiErr.code).toBe(code);
    expect(apiErr.message).toBe(message);
    expect(apiErr.unauthorized).toBe(status === 401);
    expect(apiErr.notFound).toBe(status === 404);
  });

  it('keeps v1.Error details', async () => {
    const { api } = client(respond({ code: 'locked', message: 'locked', details: { pr: 7 } }, 409));
    await expect(api.stack('x')).rejects.toMatchObject({ details: { pr: 7 } });
  });

  it('reports network failures with status 0 and the cause', async () => {
    const cause = new TypeError('Failed to fetch');
    const { api } = client(() => Promise.reject(cause));
    const err = (await api.me().catch((e: unknown) => e)) as ApiError;
    expect(err).toBeInstanceOf(ApiError);
    expect(err.status).toBe(0);
    expect(err.code).toBe(NETWORK_ERROR);
    expect(err.message).toBe('GET /v1/me: Failed to fetch');
    expect(err.cause).toBe(cause);
  });

  it('reports a success response that is not JSON', async () => {
    const { api } = client(respond('<!doctype html>', 200, 'text/html'));
    await expect(api.repos()).rejects.toMatchObject({ status: 200, code: INVALID_RESPONSE });
  });

  it('lets aborts through untouched', async () => {
    const abort = new DOMException('The operation was aborted.', 'AbortError');
    const { api } = client(() => Promise.reject(abort));
    await expect(api.me()).rejects.toBe(abort);
  });
});
