import { describe, expect, it } from 'vitest';

import type { Backend } from './api/types';
import {
  GITHUB_ORIGIN,
  githubOrigin,
  moduleBaseKey,
  moduleLabel,
  repoPath,
  s3ConsoleUrl,
  safeUrl,
  splitRepo,
  stateObjectKey,
  stateUri,
} from './format';

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

describe('stateObjectKey', () => {
  const backend: Backend = { type: 's3', bucket: 'acme-tfstate', key: 'stacks/prod/vpc/terraform.tfstate' };
  it.each<[Backend, string | undefined, string | undefined]>([
    [backend, undefined, 'stacks/prod/vpc/terraform.tfstate'],
    [backend, 'default', 'stacks/prod/vpc/terraform.tfstate'],
    [backend, 'blue', 'env:/blue/stacks/prod/vpc/terraform.tfstate'],
    [{ ...backend, workspace_key_prefix: 'workspaces' }, 'blue', 'workspaces/blue/stacks/prod/vpc/terraform.tfstate'],
    [{ type: 's3', bucket: 'acme-tfstate' }, 'blue', undefined],
  ])('%j in workspace %s is %s', (b, workspace, want) => {
    expect(stateObjectKey(b, workspace)).toBe(want);
  });
});

describe('s3ConsoleUrl and stateUri', () => {
  it('links a workspace state object with its region', () => {
    const backend: Backend = { type: 's3', bucket: 'acme tfstate', key: 'vpc/terraform.tfstate', region: 'eu-west-1' };
    expect(s3ConsoleUrl(backend, 'blue')).toBe(
      'https://s3.console.aws.amazon.com/s3/object/acme%20tfstate?region=eu-west-1&bucketType=general&prefix=env%3A%2Fblue%2Fvpc%2Fterraform.tfstate',
    );
    expect(stateUri(backend, 'blue')).toBe('s3://acme tfstate/env:/blue/vpc/terraform.tfstate');
  });

  it('links the bucket when the key is unknown', () => {
    expect(s3ConsoleUrl({ type: 's3', bucket: 'acme-tfstate' })).toBe('https://s3.console.aws.amazon.com/s3/buckets/acme-tfstate');
  });

  it.each<[Backend | undefined]>([[undefined], [{ type: 'gcs', bucket: 'b', key: 'k' }], [{ type: 's3', key: 'k' }]])(
    'has no console link for %j',
    (backend) => {
      expect(s3ConsoleUrl(backend)).toBeUndefined();
    },
  );
});

describe('module keys', () => {
  it.each([
    ['acme/infra//modules/vpc', 'acme/infra//modules/vpc'],
    ['acme/modules//vpc@v1.2.0', 'acme/modules//vpc'],
    ['gitlab.com/acme/modules//vpc@v1.2.0', 'gitlab.com/acme/modules//vpc'],
    ['registry:terraform-aws-modules/vpc/aws@5.1', 'registry:terraform-aws-modules/vpc/aws'],
    ['registry:terraform-aws-modules/vpc/aws', 'registry:terraform-aws-modules/vpc/aws'],
  ])('the base of %s is %s', (key, want) => {
    expect(moduleBaseKey(key)).toBe(want);
  });

  it.each<[Parameters<typeof moduleLabel>[0], string]>([
    [{ key: 'acme/infra//modules/vpc', kind: 'local', path: 'modules/vpc' }, 'modules/vpc'],
    [{ key: 'acme/infra//modules/vpc', kind: 'local' }, 'modules/vpc'],
    [{ key: 'acme/modules//vpc@v1.2.0', kind: 'git' }, 'acme/modules//vpc@v1.2.0'],
    [{ key: 'registry:terraform-aws-modules/vpc/aws@5.1', kind: 'registry' }, 'terraform-aws-modules/vpc/aws@5.1'],
  ])('labels %j as %s', (m, want) => {
    expect(moduleLabel(m)).toBe(want);
  });
});

describe('repository paths', () => {
  it('splits owner and name', () => {
    expect(splitRepo('acme/infra')).toEqual({ owner: 'acme', name: 'infra' });
  });

  it.each<[string, Record<string, string | undefined> | undefined, string]>([
    ['acme/infra', undefined, '/repos/acme/infra'],
    ['acme/chart.js', undefined, '/repos/acme/chart.js'],
    ['acme/infra', { ref: 'feature/x', run: undefined }, '/repos/acme/infra?ref=feature%2Fx'],
    ['acme/infra', { ref: '', run: 'r1' }, '/repos/acme/infra?run=r1'],
  ])('%s with %j is %s', (repo, query, want) => {
    expect(repoPath(repo, query)).toBe(want);
  });
});
