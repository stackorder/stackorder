import type { Page } from '@playwright/test';

import { lookup } from '../src/fixtures';

/** A request the mocked API received. */
export interface Call {
  method: string;
  path: string;
  search: string;
  body: unknown;
}

/** A canned answer that replaces the fixture for one request. */
export interface Answer {
  status?: number;
  body?: unknown;
}

/** Overrides one request; returning undefined falls through to the fixtures. */
export type Override = (call: Call) => Answer | undefined;

const avatar =
  '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><circle cx="12" cy="12" r="12" fill="#888"/></svg>';

/** Serves /v1 and /auth from the fixtures and records every call; also stubs the GitHub avatar. */
export async function mockApi(page: Page, override?: Override): Promise<Call[]> {
  const calls: Call[] = [];
  await page.route('https://avatars.githubusercontent.com/**', (route) =>
    route.fulfill({ status: 200, contentType: 'image/svg+xml', body: avatar }),
  );
  await page.route(
    (url) => url.pathname.startsWith('/v1/') || url.pathname.startsWith('/auth/'),
    async (route) => {
      const request = route.request();
      const url = new URL(request.url());
      const raw = request.postData();
      const call: Call = {
        method: request.method(),
        path: url.pathname,
        search: url.search,
        body: raw ? (JSON.parse(raw) as unknown) : undefined,
      };
      calls.push(call);
      const answer = override?.(call);
      if (answer) {
        if (answer.body === undefined) {
          await route.fulfill({ status: answer.status ?? 204 });
        } else {
          await route.fulfill({ status: answer.status ?? 200, json: answer.body });
        }
        return;
      }
      const found = lookup(call.method, url);
      if (found === undefined) {
        await route.fulfill({ status: 404, json: { code: 'not_found', message: `${call.method} ${call.path} not found` } });
        return;
      }
      await route.fulfill({ json: found });
    },
  );
  return calls;
}
