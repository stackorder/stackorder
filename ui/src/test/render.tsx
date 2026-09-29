import { render } from '@testing-library/preact';
import type { ComponentChildren } from 'preact';
import { LocationProvider } from 'preact-iso';

import { ApiClient, type FetchLike } from '../api/client';
import { ApiProvider } from '../api/context';
import { lookup } from '../fixtures';
import { ROUTER_SCOPE } from '../routerScope';

/** A request the fake API received. */
export interface Call {
  method: string;
  path: string;
  search: string;
  body: unknown;
}

/** Overrides one request; returning undefined falls through to the fixtures. */
export type Handler = (call: Call) => Response | undefined;

/** Builds a JSON response. */
export function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
}

/** Builds a text/plain response. */
export function text(body: string, status = 200): Response {
  return new Response(body, { status, headers: { 'Content-Type': 'text/plain; charset=utf-8' } });
}

/** A fetch that serves the fixtures and records every call. */
export function fakeApi(handler?: Handler): { fetch: FetchLike; calls: Call[] } {
  const calls: Call[] = [];
  const fetch: FetchLike = (input, init) => {
    const url = new URL(input, 'http://localhost');
    const method = init?.method ?? 'GET';
    const body: unknown = typeof init?.body === 'string' ? JSON.parse(init.body) : undefined;
    const call = { method, path: url.pathname, search: url.search, body };
    calls.push(call);
    const overridden = handler?.(call);
    if (overridden) return Promise.resolve(overridden);
    const found = lookup(method, url);
    if (found === undefined) {
      return Promise.resolve(json({ code: 'not_found', message: `${method} ${url.pathname} not found` }, 404));
    }
    if (typeof found === 'string') return Promise.resolve(text(found));
    return Promise.resolve(json(found));
  };
  return { fetch, calls };
}

/** Renders a tree inside the API and location providers at a given URL. */
export function renderWithApp(ui: ComponentChildren, options: { url?: string; handler?: Handler } = {}) {
  window.history.replaceState(null, '', options.url ?? '/');
  const api = fakeApi(options.handler);
  const client = new ApiClient({ fetch: api.fetch });
  const result = render(
    <ApiProvider client={client}>
      <LocationProvider scope={ROUTER_SCOPE}>{ui}</LocationProvider>
    </ApiProvider>,
  );
  return { ...result, calls: api.calls, client };
}

/** Serves a list 50 items at a time with an offset cursor, however large a limit the call asks for. */
export function paged(items: readonly unknown[], call: Call): Response {
  const start = Number(new URLSearchParams(call.search).get('cursor') ?? '0');
  const end = start + 50;
  return json(end < items.length ? { items: items.slice(start, end), next_cursor: String(end) } : { items: items.slice(start) });
}

/** Returns list[i] or fails the test when it is missing. */
export function nth<T>(list: readonly T[], i: number): T {
  const value = list[i];
  if (value === undefined) throw new Error(`expected an element at index ${String(i)} of ${String(list.length)}`);
  return value;
}
