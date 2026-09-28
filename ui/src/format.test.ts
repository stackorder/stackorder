import { describe, expect, it } from 'vitest';

import { GITHUB_ORIGIN, githubOrigin, safeUrl } from './format';

describe('safeUrl', () => {
  it.each<[string | undefined, string | undefined]>([
    ['https://github.com/acme/infra/actions/runs/1/job/2', 'https://github.com/acme/infra/actions/runs/1/job/2'],
    ['http://ghe.internal/acme/infra/pull/7', 'http://ghe.internal/acme/infra/pull/7'],
    ['HTTPS://Dashboard.Infracost.io/runs/1', 'https://dashboard.infracost.io/runs/1'],
    ['javascript:alert(1)', undefined],
    [' javascript:alert(1)', undefined],
    ['java\tscript:alert(1)', undefined],
    ['JavaScript:alert(1)', undefined],
    ['data:text/html,<script>alert(1)</script>', undefined],
    ['vbscript:msgbox(1)', undefined],
    ['/runs/relative', undefined],
    ['not a url', undefined],
    ['', undefined],
    [undefined, undefined],
  ])('%j is %j', (link, want) => {
    expect(safeUrl(link)).toBe(want);
  });
});

describe('githubOrigin', () => {
  it.each<[string | undefined, string]>([
    ['https://github.com/acme/infra/pull/42', 'https://github.com'],
    ['https://ghe.acme.internal/acme/infra/actions/runs/1', 'https://ghe.acme.internal'],
    ['javascript:alert(1)', GITHUB_ORIGIN],
    ['nonsense', GITHUB_ORIGIN],
    [undefined, GITHUB_ORIGIN],
  ])('%j is %s', (link, want) => {
    expect(githubOrigin(link)).toBe(want);
  });
});
