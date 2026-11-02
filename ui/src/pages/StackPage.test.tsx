import { fireEvent, screen, waitFor, within } from '@testing-library/preact';
import { describe, expect, it } from 'vitest';

import type { StackDetail, StackResource, StackResources } from '../api/types';
import { Routes } from '../app';
import { clone, ids, stack, stackResources } from '../fixtures';
import { type Handler, json, nth, paged, renderWithApp } from '../test/render';
import {
  driftedAddresses,
  groupResources,
  groupStartsOpen,
  matchesResource,
  relativeAddress,
  resourceFlagCounts,
  StackPage,
} from './StackPage';

const url = `/stacks/${ids.stack}`;
const page = <StackPage id={ids.stack} />;

function card(name: string) {
  return within(screen.getByRole('region', { name }));
}

describe('StackPage', () => {
  it('shows the header with environment, tool and a link to the state object', async () => {
    renderWithApp(page, { url });
    expect(await screen.findByRole('heading', { level: 1, name: 'stacks/prod/eks' })).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'acme/infra' })).toHaveAttribute('href', '/repos/acme/infra');
    expect(screen.getByText('production')).toBeInTheDocument();
    expect(screen.getByText('tofu')).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 's3://acme-tfstate/stacks/prod/eks/terraform.tfstate' })).toHaveAttribute(
      'href',
      'https://s3.console.aws.amazon.com/s3/object/acme-tfstate?region=eu-west-1&bucketType=general&prefix=stacks%2Fprod%2Feks%2Fterraform.tfstate',
    );
    await waitFor(() => {
      expect(document.title).toBe('stacks/prod/eks · Stackorder');
    });
  });

  it('shows the last apply, last plan, drift and lock cards', async () => {
    renderWithApp(page, { url });
    await screen.findByRole('heading', { level: 1 });
    const apply = card('Last apply');
    expect(apply.getByText('applied')).toBeInTheDocument();
    expect(apply.getByText('+2')).toBeInTheDocument();
    expect(apply.getByRole('link', { name: '#38' })).toHaveAttribute('href', 'https://github.com/acme/infra/pull/38');
    expect(apply.getByRole('link', { name: '1a2b3c4' })).toHaveAttribute(
      'href',
      'https://github.com/acme/infra/commit/1a2b3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8f9a0b',
    );
    expect(apply.getByRole('link', { name: 'Job log' })).toHaveAttribute(
      'href',
      'https://github.com/acme/infra/actions/runs/17311207/job/48533127',
    );

    const plan = card('Last plan');
    expect(plan.getByText('planned')).toBeInTheDocument();
    expect(plan.getByText('0 to add, 1 to change, 0 to destroy, 1 to replace')).toBeInTheDocument();
    expect(plan.getByRole('link', { name: '2c4e6a80' })).toHaveAttribute('href', `/runs/${ids.planRun}`);

    const drift = card('Drift');
    expect(drift.getByText('drifted')).toHaveClass('badge--warning');
    expect(drift.getByRole('link', { name: '#57' })).toHaveAttribute('href', 'https://github.com/acme/infra/issues/57');

    const lock = card('Lock');
    expect(lock.getByText('locked')).toBeInTheDocument();
    expect(lock.getByRole('link', { name: 'PR #42' })).toHaveAttribute('href', 'https://github.com/acme/infra/pull/42');
    expect(lock.getByText('apply of PR #42')).toBeInTheDocument();
    expect(lock.getByRole('button', { name: 'Unlock…' })).toBeInTheDocument();
  });

  it('links dependencies and dependents, including other repositories', async () => {
    renderWithApp(page, { url });
    await screen.findByRole('heading', { level: 1 });
    await waitFor(() => {
      expect(card('Depends on').getByRole('link', { name: 'stacks/prod/vpc' })).toHaveAttribute(
        'href',
        `/stacks/${ids.stacks['stacks/prod/vpc'] ?? ''}`,
      );
    });
    const dependents = card('Depended on by');
    expect(dependents.getByRole('link', { name: 'stacks/prod/apps' })).toHaveAttribute(
      'href',
      `/stacks/${ids.stacks['stacks/prod/apps'] ?? ''}`,
    );
    expect(dependents.getByRole('link', { name: 'acme/platform-infra//stacks/prod/observability' })).toHaveAttribute(
      'href',
      '/repos/acme/platform-infra',
    );
  });

  it('links a dependency that is past the first page of the repository stacks', async () => {
    const far = { id: 'f0e1d2c3-b4a5-4968-8776-655443322110', repo: 'acme/infra', key: 'stacks/zz/vpc', path: 'stacks/zz/vpc' };
    const siblings = [
      ...Array.from({ length: 60 }, (_, i) => ({
        id: `00000000-0000-4000-8000-${String(i).padStart(12, '0')}`,
        repo: 'acme/infra',
        key: `stacks/a${String(i).padStart(2, '0')}`,
        path: `stacks/a${String(i).padStart(2, '0')}`,
      })),
      far,
    ];
    const s = clone(stack);
    s.depends_on = [far.key];
    renderWithApp(page, {
      url,
      handler: (c) => {
        if (c.path === `/v1/stacks/${ids.stack}`) return json(s);
        return c.path === '/v1/repos/acme/infra/stacks' ? paged(siblings, c) : undefined;
      },
    });
    await screen.findByRole('heading', { level: 1 });
    await waitFor(() => {
      expect(card('Depends on').getByRole('link', { name: far.key })).toHaveAttribute('href', `/stacks/${far.id}`);
    });
  });

  it('falls back to the repository graph when stack ids are unavailable', async () => {
    renderWithApp(page, {
      url,
      handler: (c) => (c.path.endsWith('/stacks') ? json({ code: 'internal', message: 'x' }, 500) : undefined),
    });
    await screen.findByRole('heading', { level: 1 });
    expect(card('Depends on').getByRole('link', { name: 'stacks/prod/vpc' })).toHaveAttribute('href', '/repos/acme/infra');
  });

  it('lists pinned module versions and how far behind they are', async () => {
    renderWithApp(page, { url });
    const table = await screen.findByRole('table', { name: 'Modules used by stacks/prod/eks' });
    const rows = within(table).getAllByRole('row').slice(1);
    expect(rows.map((r) => [...r.querySelectorAll('td')].map((td) => td.textContent))).toEqual([
      ['acme/infra//modules/eks', 'local', '—', '—'],
      ['acme/terraform-modules//eks-addons', 'v0.8.0', 'v0.10.0', '2 versions behind'],
    ]);
    expect(within(nth(rows, 1)).getByRole('link')).toHaveAttribute(
      'href',
      `/modules?q=${encodeURIComponent('acme/terraform-modules//eks-addons')}`,
    );
  });

  it('shows the run history', async () => {
    renderWithApp(page, { url });
    const table = await screen.findByRole('table', { name: 'Run history of stacks/prod/eks' });
    const rows = within(table).getAllByRole('row').slice(1);
    expect(rows).toHaveLength(4);
    expect(within(nth(rows, 0)).getByText('failed')).toBeInTheDocument();
    expect(within(nth(rows, 2)).getAllByText('—')).toHaveLength(1);
    expect(within(nth(rows, 3)).getByRole('link', { name: '#38' })).toBeInTheDocument();
  });

  it('unlocks with a reason, posts it, and refreshes', async () => {
    let unlocked = false;
    const handler: Handler = (c) => {
      if (c.method === 'POST' && c.path === `/v1/stacks/${ids.stack}/unlock`) {
        unlocked = true;
        return json({ released: [stack.lock] });
      }
      if (c.path === `/v1/stacks/${ids.stack}` && unlocked) {
        const s: StackDetail = clone(stack);
        delete s.lock;
        return json(s);
      }
      return undefined;
    };
    const { calls } = renderWithApp(page, { url, handler });
    fireEvent.click(await screen.findByRole('button', { name: 'Unlock…' }));

    const dialog = screen.getByRole('dialog', { name: 'Unlock stacks/prod/eks' });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Unlock' }));
    expect(within(dialog).getByRole('alert')).toHaveTextContent('Give a reason');
    expect(calls.some((c) => c.method === 'POST')).toBe(false);

    fireEvent.input(within(dialog).getByLabelText('Reason'), { target: { value: '  runner died mid-apply; state checked by hand  ' } });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Unlock' }));

    expect(await screen.findByText('Released 1 lock held by PR #42.')).toBeInTheDocument();
    const post = calls.find((c) => c.method === 'POST');
    expect(post).toEqual({
      method: 'POST',
      path: `/v1/stacks/${ids.stack}/unlock`,
      search: '',
      body: { reason: 'runner died mid-apply; state checked by hand' },
    });
    expect(await card('Lock').findByText('Not locked.')).toBeInTheDocument();
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
    expect(calls.filter((c) => c.path === `/v1/stacks/${ids.stack}`)).toHaveLength(2);
  });

  it('does not carry the unlock message over to the next stack', async () => {
    const vpcId = ids.stacks['stacks/prod/vpc'] ?? '';
    const vpc: StackDetail = { id: vpcId, repo: 'acme/infra', key: 'stacks/prod/vpc', path: 'stacks/prod/vpc' };
    const handler: Handler = (c) => {
      if (c.method === 'POST') return json({ released: [stack.lock] });
      if (c.path === `/v1/stacks/${vpcId}`) return json(vpc);
      if (c.path === `/v1/stacks/${vpcId}/runs`) return json({ items: [] });
      return undefined;
    };
    renderWithApp(<Routes />, { url, handler });
    fireEvent.click(await screen.findByRole('button', { name: 'Unlock…' }));
    fireEvent.input(screen.getByLabelText('Reason'), { target: { value: 'stale lock' } });
    fireEvent.click(screen.getByRole('button', { name: 'Unlock' }));
    expect(await screen.findByText('Released 1 lock held by PR #42.')).toBeInTheDocument();

    fireEvent.click(await card('Depends on').findByRole('link', { name: 'stacks/prod/vpc' }));
    expect(await screen.findByRole('heading', { level: 1, name: 'stacks/prod/vpc' })).toBeInTheDocument();
    expect(window.location.pathname).toBe(`/stacks/${vpcId}`);
    expect(screen.queryByText('Released 1 lock held by PR #42.')).not.toBeInTheDocument();
    expect(card('Lock').getByText('Not locked.')).toBeInTheDocument();
  });

  it('confirms an unlock the server answers without a body', async () => {
    renderWithApp(page, { url, handler: (c) => (c.method === 'POST' ? new Response(null, { status: 204 }) : undefined) });
    fireEvent.click(await screen.findByRole('button', { name: 'Unlock…' }));
    fireEvent.input(screen.getByLabelText('Reason'), { target: { value: 'stale lock' } });
    fireEvent.click(screen.getByRole('button', { name: 'Unlock' }));
    expect(await screen.findByText('Lock released.')).toBeInTheDocument();
  });

  it('keeps the dialog open with the error when unlocking is refused', async () => {
    renderWithApp(page, {
      url,
      handler: (c) =>
        c.method === 'POST' ? json({ code: 'forbidden', message: 'write permission on acme/infra required' }, 403) : undefined,
    });
    fireEvent.click(await screen.findByRole('button', { name: 'Unlock…' }));
    const dialog = screen.getByRole('dialog');
    fireEvent.input(within(dialog).getByLabelText('Reason'), { target: { value: 'stale' } });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Unlock' }));
    expect(await within(dialog).findByRole('alert')).toHaveTextContent('write permission on acme/infra required');
    fireEvent.click(within(dialog).getByRole('button', { name: 'Cancel' }));
    await waitFor(() => {
      expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
    });
    expect(card('Lock').getByText('locked')).toBeInTheDocument();
  });

  it('never turns an API URL with another scheme into a link', async () => {
    const hostile: StackDetail = clone(stack);
    if (hostile.last_apply) hostile.last_apply.job_url = 'javascript:alert(1)';
    if (hostile.drift) hostile.drift.issue_url = 'data:text/html,<script>alert(2)</script>';
    const history = { items: [{ run_id: ids.run, sha: 'abc', status: 'failed', job_url: 'vbscript:msgbox(3)' }] };
    renderWithApp(page, {
      url,
      handler: (c) =>
        c.path === `/v1/stacks/${ids.stack}` ? json(hostile) : c.path === `/v1/stacks/${ids.stack}/runs` ? json(history) : undefined,
    });
    await screen.findByRole('table', { name: 'Run history of stacks/prod/eks' });
    expect(card('Last apply').queryByRole('link', { name: 'Job log' })).not.toBeInTheDocument();
    expect(card('Drift').getByRole('link', { name: '#57' })).toHaveAttribute('href', 'https://github.com/acme/infra/issues/57');
    expect(screen.queryByRole('link', { name: 'log' })).not.toBeInTheDocument();
    for (const a of document.querySelectorAll('a[href]')) {
      expect(a.getAttribute('href')).toMatch(/^(\/|https?:\/\/)/);
    }
  });

  it('renders a stack without a lock, drift, plans or modules', async () => {
    const bare: StackDetail = { id: ids.stack, repo: 'acme/infra', key: 'stacks/dev/sandbox:blue', path: 'stacks/dev/sandbox', workspace: 'blue' };
    renderWithApp(page, { url, handler: (c) => (c.path === `/v1/stacks/${ids.stack}` ? json(bare) : undefined) });
    expect(await screen.findByRole('heading', { level: 1, name: 'stacks/dev/sandbox:blue' })).toBeInTheDocument();
    expect(screen.getByText('Never applied through Stackorder.')).toBeInTheDocument();
    expect(screen.getByText('No plan recorded yet.')).toBeInTheDocument();
    expect(screen.getByText('No drift check has run yet.')).toBeInTheDocument();
    expect(screen.getByText('Not locked.')).toBeInTheDocument();
    expect(screen.getByText('No dependencies.')).toBeInTheDocument();
    expect(screen.getByText('Nothing depends on this stack.')).toBeInTheDocument();
    expect(screen.getByText('This stack uses no modules.')).toBeInTheDocument();
    expect(screen.getByText('blue')).toBeInTheDocument();
    expect(screen.getByText('default')).toBeInTheDocument();
  });

  it('shows the instance next to the workspace', async () => {
    const instance: StackDetail = {
      id: ids.stack,
      repo: 'acme/infra',
      key: 'infra/kyc:production',
      path: 'infra/kyc',
      instance: 'production',
      workspace: 'prod',
      environment: 'kyc-production',
      backend: { type: 's3', bucket: 'acme-tfstate', region: 'eu-west-1' },
    };
    renderWithApp(page, { url, handler: (c) => (c.path === `/v1/stacks/${ids.stack}` ? json(instance) : undefined) });
    expect(await screen.findByRole('heading', { level: 1, name: 'infra/kyc:production' })).toBeInTheDocument();
    expect(screen.getByText('Instance').nextElementSibling).toHaveTextContent('production');
    expect(screen.getByText('Workspace').nextElementSibling).toHaveTextContent('prod');
    expect(screen.getByText('kyc-production')).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 's3://acme-tfstate' })).toHaveAttribute(
      'href',
      'https://s3.console.aws.amazon.com/s3/buckets/acme-tfstate?region=eu-west-1',
    );
  });

  it('links the state object of an instance whose backend key is known', async () => {
    const instance: StackDetail = {
      id: ids.stack,
      repo: 'acme/infra',
      key: 'infra/kyc:staging',
      path: 'infra/kyc',
      instance: 'staging',
      backend: { type: 's3', bucket: 'acme-tfstate', key: 'kyc/staging.tfstate', region: 'eu-west-1' },
    };
    renderWithApp(page, { url, handler: (c) => (c.path === `/v1/stacks/${ids.stack}` ? json(instance) : undefined) });
    expect(await screen.findByRole('link', { name: 's3://acme-tfstate/kyc/staging.tfstate' })).toHaveAttribute(
      'href',
      'https://s3.console.aws.amazon.com/s3/object/acme-tfstate?region=eu-west-1&bucketType=general&prefix=kyc%2Fstaging.tfstate',
    );
    expect(screen.getByText('Instance').nextElementSibling).toHaveTextContent('staging');
    expect(screen.queryByText('Workspace')).not.toBeInTheDocument();
  });

  it('shows not found for an unknown stack', async () => {
    renderWithApp(<StackPage id="00000000-0000-4000-8000-000000000000" />, { url: '/stacks/00000000-0000-4000-8000-000000000000' });
    expect(await screen.findByText('This stack does not exist or you do not have access to it.')).toBeInTheDocument();
  });
});

const resourcesPath = `/v1/stacks/${ids.stack}/resources`;
const resourceItems = stackResources.items ?? [];
const nodeGroup = 'module.eks.aws_eks_node_group.default';

function resources() {
  return within(screen.getByRole('region', { name: 'Resources' }));
}

async function findResources() {
  return within(await screen.findByRole('region', { name: 'Resources' }));
}

function withResources(list: StackResources, detail?: StackDetail): Handler {
  return (c) => {
    if (c.path === resourcesPath) return json(list);
    if (detail && c.path === `/v1/stacks/${ids.stack}`) return json(detail);
    return undefined;
  };
}

function cells(table: HTMLElement): string[][] {
  return within(table)
    .getAllByRole('row')
    .slice(1)
    .map((r) => [...r.querySelectorAll('td')].map((td) => td.textContent));
}

function resource(address: string, extra: Partial<StackResource> = {}): StackResource {
  return { address, type: address.split('.').at(-2) ?? 'null_resource', ...extra };
}

function summaries(): string[] {
  return [...screen.getByRole('region', { name: 'Resources' }).querySelectorAll('summary')].map((el) => el.textContent);
}

function openGroups(): string[] {
  return [...screen.getByRole('region', { name: 'Resources' }).querySelectorAll('details')]
    .filter((d) => d.open)
    .map((d) => d.querySelector('summary')?.firstChild?.textContent ?? '');
}

describe('resource helpers', () => {
  it.each([
    ['', true],
    ['  ', true],
    ['NODE_GROUP', true],
    ['module.eks.aws_eks', true],
    ['aws_eks_node_group', true],
    ['registry.opentofu.org', false],
    ['vpc', false],
  ])('filter %j matches: %s', (q, want) => {
    expect(matchesResource({ address: nodeGroup }, q)).toBe(want);
  });

  it.each([
    [{ address: 'random_id.cluster_suffix' }, 'random_id.cluster_suffix'],
    [{ address: nodeGroup, module: 'module.eks' }, 'aws_eks_node_group.default'],
    [{ address: 'module.eks.module.addons.helm_release.this["a.b/c"]', module: 'module.eks.module.addons' }, 'helm_release.this["a.b/c"]'],
    [{ address: 'module.db["eu.west"].aws_db_instance.this', module: 'module.db["eu.west"]' }, 'aws_db_instance.this'],
    [{ address: 'aws_s3_bucket.logs', module: 'module.other' }, 'aws_s3_bucket.logs'],
  ])('relative address of %j is %s', (r, want) => {
    expect(relativeAddress(r)).toBe(want);
  });

  it('groups by module with the root module first and module addresses in order', () => {
    const groups = groupResources([
      resource('module.b.aws_s3_bucket.x', { module: 'module.b' }),
      resource('aws_s3_bucket.root'),
      resource('module.a.module.c.aws_s3_bucket.y', { module: 'module.a.module.c' }),
      resource('module.a.aws_s3_bucket.z', { module: 'module.a' }),
      resource('random_id.suffix'),
    ]);
    expect(groups.map((g) => [g.module, g.items.map((r) => r.address)])).toEqual([
      ['', ['aws_s3_bucket.root', 'random_id.suffix']],
      ['module.a', ['module.a.aws_s3_bucket.z']],
      ['module.a.module.c', ['module.a.module.c.aws_s3_bucket.y']],
      ['module.b', ['module.b.aws_s3_bucket.x']],
    ]);
    expect(groupResources([])).toEqual([]);
  });

  it('takes drifted addresses from every list of a drifted summary only', () => {
    const summary = { adds: 1, changes: 1, destroys: 1, replaces: 1, added: ['a'], changed: ['c'], destroyed: ['d'], replaced: ['r'] };
    expect([...driftedAddresses({ checked_at: '2026-09-28T06:05:44Z', drifted: true, summary })].sort()).toEqual(['a', 'c', 'd', 'r']);
    expect(driftedAddresses({ checked_at: '2026-09-28T06:05:44Z', drifted: false, summary }).size).toBe(0);
    expect(driftedAddresses({ checked_at: '2026-09-28T06:05:44Z', drifted: true }).size).toBe(0);
    expect(driftedAddresses(undefined).size).toBe(0);
  });

  it('counts drifted, tainted and deposed resources', () => {
    const items = [
      resource('a.one', { tainted: true, deposed: true }),
      resource('a.two', { tainted: true }),
      resource('a.three'),
    ];
    expect(resourceFlagCounts(items, new Set(['a.three', 'a.missing']))).toEqual({ drifted: 1, tainted: 2, deposed: 1 });
    expect(resourceFlagCounts([], new Set())).toEqual({ drifted: 0, tainted: 0, deposed: 0 });
  });

  it.each([
    [{ drifted: 0, tainted: 0, deposed: 0 }, false, 50, true],
    [{ drifted: 0, tainted: 0, deposed: 0 }, false, 51, false],
    [{ drifted: 0, tainted: 0, deposed: 0 }, true, 500, true],
    [{ drifted: 1, tainted: 0, deposed: 0 }, false, 500, true],
    [{ drifted: 0, tainted: 1, deposed: 0 }, false, 500, true],
    [{ drifted: 0, tainted: 0, deposed: 1 }, false, 500, true],
  ])('a group with %j, filtering %s and %i rows shown starts open: %s', (flags, filtering, shown, want) => {
    expect(groupStartsOpen(flags, filtering, shown)).toBe(want);
  });
});

describe('StackPage resources', () => {
  it('groups the resources by module with flag counts and badges', async () => {
    renderWithApp(page, { url });
    const root = await screen.findByRole('table', { name: 'Resources in Root module of stacks/prod/eks' });
    expect(summaries()).toEqual([
      'Root module 2 resources',
      'module.eks 7 resources 1 drifted 1 tainted',
      'module.eks.module.addons 2 resources',
    ]);
    expect(openGroups()).toEqual(['Root module', 'module.eks', 'module.eks.module.addons']);
    expect(cells(root)).toEqual([
      ['aws_security_group_rule.cluster_ingress_vpn', '—'],
      ['random_id.cluster_suffix', '—'],
    ]);
    const eks = screen.getByRole('table', { name: 'Resources in module.eks of stacks/prod/eks' });
    expect(cells(eks).map((r) => r[0])).toEqual([
      'aws_eks_addon.coredns',
      'aws_eks_addon.kube_proxy',
      'aws_eks_cluster.this',
      'aws_eks_node_group.default drifted',
      'aws_iam_role.cluster',
      'aws_iam_role.nodes',
      'aws_launch_template.nodes tainted',
    ]);
    expect(within(eks).getByText('aws_eks_node_group.default')).toHaveAttribute('title', nodeGroup);
    expect(within(eks).getByText('drifted')).toHaveClass('badge--warning');
    expect(within(eks).getByText('tainted')).toHaveClass('badge--danger');
    const addons = screen.getByRole('table', { name: 'Resources in module.eks.module.addons of stacks/prod/eks' });
    expect(cells(addons).map((r) => r[0])).toEqual([
      'helm_release.this["aws-load-balancer-controller"]',
      'helm_release.this["metrics-server"]',
    ]);
    expect(resources().getByText('1 drifted')).toHaveClass('badge--warning');
    expect(resources().getByText('1 tainted')).toHaveClass('badge--danger');
  });

  it('marks a deposed object and a drifted address that left the state', async () => {
    const list = clone(stackResources);
    const items = list.items ?? [];
    const cluster = items.find((r) => r.address === 'module.eks.aws_eks_cluster.this');
    if (cluster) cluster.deposed = true;
    const s = clone(stack);
    if (s.drift?.summary) s.drift.summary.added = ['random_id.cluster_suffix'];
    renderWithApp(page, { url, handler: withResources(list, s) });
    const root = await screen.findByRole('table', { name: 'Resources in Root module of stacks/prod/eks' });
    expect(within(root).getByText('drifted')).toHaveClass('badge--warning');
    const eks = screen.getByRole('table', { name: 'Resources in module.eks of stacks/prod/eks' });
    expect(within(eks).getByText('deposed')).toHaveClass('badge--warning');
    expect(summaries()).toEqual([
      'Root module 2 resources 1 drifted',
      'module.eks 7 resources 1 drifted 1 tainted 1 deposed',
      'module.eks.module.addons 2 resources',
    ]);
  });

  it('shows no drifted badge when the latest drift check found none', async () => {
    const s = clone(stack);
    if (s.drift) s.drift.drifted = false;
    renderWithApp(page, { url, handler: withResources(stackResources, s) });
    const eks = await screen.findByRole('table', { name: 'Resources in module.eks of stacks/prod/eks' });
    expect(within(eks).queryByText('drifted')).not.toBeInTheDocument();
    expect(summaries()[1]).toBe('module.eks 7 resources 1 tainted');
  });

  it('shows the run, pull request and time of each resource’s last change', async () => {
    renderWithApp(page, { url });
    const eks = await screen.findByRole('table', { name: 'Resources in module.eks of stacks/prod/eks' });
    expect(within(eks).getAllByRole('columnheader').map((th) => th.textContent)).toEqual(['Resource', 'Last change']);
    const coredns = nth(within(eks).getAllByRole('row'), 1);
    expect(nth([...coredns.querySelectorAll('td')], 1).textContent).toMatch(/^created 9e1a3c5e #38 \S.*$/);
    expect(within(coredns).getByRole('link', { name: '9e1a3c5e' })).toHaveAttribute('href', '/runs/9e1a3c5e-7b9d-4f1a-a3c5-e7b9d1f3a588');
    expect(within(coredns).getByRole('link', { name: '#38' })).toHaveAttribute('href', 'https://github.com/acme/infra/pull/38');
    expect(coredns.querySelector('time')).toHaveAttribute('datetime', '2026-09-21T14:12:03Z');
    const cluster = nth(within(eks).getAllByRole('row'), 3);
    expect(within(cluster).getByText('—')).toHaveClass('muted');
  });

  it('shows a change without a pull request and leaves the column out when nothing has a last change', async () => {
    const list = clone(stackResources);
    for (const r of list.items ?? []) delete r.last_change;
    const first = nth(list.items ?? [], 1);
    first.last_change = { action: 'replace', run_id: ids.run, sha: 'abc' };
    const { unmount } = renderWithApp(page, { url, handler: withResources(list) });
    const eks = await screen.findByRole('table', { name: 'Resources in module.eks of stacks/prod/eks' });
    const row = nth(within(eks).getAllByRole('row'), 1);
    expect(nth([...row.querySelectorAll('td')], 1).textContent).toBe('replaced 7d9f1b3c —');
    unmount();

    delete first.last_change;
    renderWithApp(page, { url, handler: withResources(list) });
    const plain = await screen.findByRole('table', { name: 'Resources in module.eks of stacks/prod/eks' });
    expect(within(plain).getAllByRole('columnheader').map((th) => th.textContent)).toEqual(['Resource']);
  });

  it.each<[string, Partial<StackResources>, Partial<StackDetail>, RegExp]>([
    ['a drift check', {}, {}, /^From the state at drift check d2f4a6c8, [^,]+\.$/],
    [
      'an apply',
      { run_id: '9e1a3c5e-7b9d-4f1a-a3c5-e7b9d1f3a588', mode: 'apply', reported_at: '2026-09-21T14:12:03Z' },
      {},
      /^From the state after apply 9e1a3c5e, [^,]+\.$/,
    ],
    [
      'a failed apply',
      { run_id: '7d9f1b3c-5e7a-4b9c-8d1e-3f5a7b9c1d66', mode: 'apply', reported_at: '2026-09-28T09:04:57Z' },
      {},
      /^From the state after failed apply 7d9f1b3c, [^,]+\.$/,
    ],
    [
      'a drift check before the last apply',
      {},
      { last_apply: { ...stack.last_apply, run_id: ids.run, sha: 'abc', status: 'applied', finished_at: '2026-09-29T10:00:00Z' } },
      /^From the state at drift check d2f4a6c8, [^,]+, before the last apply\.$/,
    ],
    [
      'an empty state',
      { run_id: '9e1a3c5e-7b9d-4f1a-a3c5-e7b9d1f3a588', mode: 'apply', total: 0, items: [] },
      {},
      /^No resources in the state after apply 9e1a3c5e, [^,]+\.$/,
    ],
    ['an empty state at a drift check', { total: 0, items: [] }, {}, /^No resources in the state at drift check d2f4a6c8, [^,]+\.$/],
  ])('says where the list comes from after %s', async (_name, change, detail, want) => {
    const list = { ...clone(stackResources), ...change };
    renderWithApp(page, { url, handler: withResources(list, { ...clone(stack), ...detail }) });
    await screen.findByRole('heading', { level: 1 });
    await waitFor(() => {
      expect(resources().getByText(/the state/)).toHaveTextContent(want);
    });
    const runId = list.run_id ?? '';
    expect(resources().getAllByRole('link', { name: runId.slice(0, 8) })[0]).toHaveAttribute('href', `/runs/${runId}`);
    if (list.total === 0) {
      expect(resources().queryByRole('search')).not.toBeInTheDocument();
      expect(resources().queryByRole('table')).not.toBeInTheDocument();
    }
  });

  it('keeps a recorded list whose run was deleted', async () => {
    const list = { ...clone(stackResources) };
    delete list.run_id;
    renderWithApp(page, { url, handler: withResources(list) });
    await screen.findByRole('heading', { level: 1 });
    await waitFor(() => {
      expect(resources().getByText(/the state/)).toHaveTextContent(/^From the state at drift check, [^,]+\.$/);
    });
    expect(resources().queryByRole('link', { name: /^d2f4a6c8/ })).not.toBeInTheDocument();
    expect(resources().getByText('11 of 11')).toBeInTheDocument();
  });

  it('says only that no list has been reported yet', async () => {
    renderWithApp(page, { url, handler: withResources({ total: 0, items: [] }) });
    expect(await (await findResources()).findByText('No resource list yet.')).toHaveClass('empty');
    const region = screen.getByRole('region', { name: 'Resources' });
    expect(region.textContent).toBe('ResourcesNo resource list yet.');
  });

  it('warns that a truncated list is shown in part', async () => {
    const list = { ...clone(stackResources), total: 4812, truncated: true };
    renderWithApp(page, { url, handler: withResources(list) });
    expect(await (await findResources()).findByText('Showing the first 11 of 4812 resources, by address.')).toHaveClass('text-warning');
    expect(resources().getByText('11 of 11')).toBeInTheDocument();
  });

  it('shows only the provenance and the warning when a truncated list has no items', async () => {
    renderWithApp(page, { url, handler: withResources({ ...clone(stackResources), total: 812, truncated: true, items: [] }) });
    expect(await (await findResources()).findByText('Showing the first 0 of 812 resources, by address.')).toHaveClass('text-warning');
    expect(resources().getByText(/the state/)).toHaveTextContent(/^From the state at drift check d2f4a6c8/);
    expect(resources().queryByText(/No resources/)).not.toBeInTheDocument();
    expect(resources().queryByRole('search')).not.toBeInTheDocument();
  });

  it('filters by address from the URL and as the user types', async () => {
    renderWithApp(page, { url: `${url}?q=NODE_GROUP` });
    const eks = await screen.findByRole('table', { name: 'Resources in module.eks of stacks/prod/eks' });
    expect(cells(eks).map((r) => r[0])).toEqual(['aws_eks_node_group.default drifted']);
    expect(summaries()).toEqual(['module.eks 1 resource 1 drifted']);
    expect(resources().getByText('1 of 11')).toHaveAttribute('aria-live', 'polite');
    expect(resources().getByLabelText('Filter')).toHaveValue('NODE_GROUP');

    fireEvent.input(resources().getByLabelText('Filter'), { target: { value: 'helm' } });
    await waitFor(() => {
      expect(window.location.search).toBe('?q=helm');
    });
    expect(window.location.pathname).toBe(url);
    expect(await resources().findByText('2 of 11')).toBeInTheDocument();
    expect(summaries()).toEqual(['module.eks.module.addons 2 resources']);

    fireEvent.input(resources().getByLabelText('Filter'), { target: { value: 'aws_vpc' } });
    expect(await resources().findByText('No resource matches the filter.')).toBeInTheDocument();
    expect(resources().getByText('0 of 11')).toBeInTheDocument();

    fireEvent.input(resources().getByLabelText('Filter'), { target: { value: '' } });
    await waitFor(() => {
      expect(window.location.search).toBe('');
    });
    expect(await resources().findByText('11 of 11')).toBeInTheDocument();
  });

  it('opens flagged groups, closes the rest of a long list and keeps the user’s toggles across filters', async () => {
    const items = [
      ...Array.from({ length: 30 }, (_, i) => resource(`aws_s3_bucket.b${String(i).padStart(2, '0')}`)),
      resource('module.eks.aws_eks_cluster.this', { module: 'module.eks' }),
      resource(nodeGroup, { module: 'module.eks' }),
      ...Array.from({ length: 25 }, (_, i) =>
        resource(`module.logs.aws_s3_bucket.l${String(i).padStart(2, '0')}`, { module: 'module.logs' }),
      ),
      resource('module.tls.tls_private_key.this', { module: 'module.tls', tainted: true }),
    ];
    renderWithApp(<Routes />, { url, handler: withResources({ ...clone(stackResources), total: items.length, items }) });
    await screen.findByRole('table', { name: 'Resources in module.eks of stacks/prod/eks' });
    expect(openGroups()).toEqual(['module.eks', 'module.tls']);
    expect(screen.queryByRole('table', { name: 'Resources in Root module of stacks/prod/eks' })).not.toBeInTheDocument();
    expect(screen.queryByRole('table', { name: 'Resources in module.logs of stacks/prod/eks' })).not.toBeInTheDocument();

    const summary = (name: string) => {
      const found = [...screen.getByRole('region', { name: 'Resources' }).querySelectorAll('summary')].find(
        (el) => el.firstChild?.textContent === name,
      );
      if (!found) throw new Error(`no group ${name}`);
      return found;
    };
    fireEvent.click(summary('Root module'));
    expect(await screen.findByRole('table', { name: 'Resources in Root module of stacks/prod/eks' })).toBeInTheDocument();
    fireEvent.click(summary('module.eks'));
    expect(openGroups()).toEqual(['Root module', 'module.tls']);

    fireEvent.input(resources().getByLabelText('Filter'), { target: { value: 'aws_' } });
    expect(await resources().findByText('57 of 58')).toBeInTheDocument();
    expect(openGroups()).toEqual(['Root module', 'module.logs']);
    fireEvent.click(summary('module.logs'));
    fireEvent.click(summary('Root module'));
    expect(openGroups()).toEqual([]);
    fireEvent.click(summary('Root module'));

    fireEvent.input(resources().getByLabelText('Filter'), { target: { value: '' } });
    expect(await resources().findByText('58 of 58')).toBeInTheDocument();
    expect(openGroups()).toEqual(['Root module', 'module.tls']);
  });

  it('opens every group of a long list while a filter is active', async () => {
    const items = Array.from({ length: 60 }, (_, i) =>
      resource(`module.m${String(i % 3)}.aws_s3_bucket.b${String(i).padStart(2, '0')}`, { module: `module.m${String(i % 3)}` }),
    );
    renderWithApp(page, { url: `${url}?q=aws_s3`, handler: withResources({ ...clone(stackResources), total: 60, items }) });
    expect(await (await findResources()).findByText('60 of 60')).toBeInTheDocument();
    expect(openGroups()).toEqual(['module.m0', 'module.m1', 'module.m2']);
  });

  it('shows a single module without a group card', async () => {
    const items = resourceItems.filter((r) => r.module === 'module.eks');
    renderWithApp(page, { url, handler: withResources({ ...clone(stackResources), total: items.length, items }) });
    const table = await screen.findByRole('table', { name: 'Resources of stacks/prod/eks' });
    expect(nth(cells(table), 3)[0]).toBe(`${nodeGroup} drifted`);
    expect(screen.getByRole('region', { name: 'Resources' }).querySelector('details')).toBeNull();
  });

  it('keeps the rest of the page when the resource list fails to load', async () => {
    let fail = true;
    renderWithApp(page, {
      url,
      handler: (c) => (c.path === resourcesPath && fail ? json({ code: 'internal', message: 'database unavailable' }, 500) : undefined),
    });
    expect(await (await findResources()).findByRole('alert')).toHaveTextContent('database unavailable');
    expect(screen.getByRole('heading', { level: 1, name: 'stacks/prod/eks' })).toBeInTheDocument();
    expect(await screen.findByRole('table', { name: 'Run history of stacks/prod/eks' })).toBeInTheDocument();
    expect(card('Drift').getByText('drifted')).toBeInTheDocument();
    fail = false;
    fireEvent.click(resources().getByRole('button', { name: 'Try again' }));
    expect(await screen.findByRole('table', { name: 'Resources in module.eks of stacks/prod/eks' })).toBeInTheDocument();
  });

  it('renders a hostile address and module as text', async () => {
    const module = 'module.x["a\\"><img src=x onerror=alert(1)>"]';
    const address = `${module}.null_resource.this["<script>alert(2)</script>"]`;
    const items = [resource('aws_s3_bucket.logs'), resource(address, { module, type: 'null_resource' })];
    renderWithApp(page, { url, handler: withResources({ ...clone(stackResources), total: 2, items }) });
    const table = await screen.findByRole('table', { name: `Resources in ${module} of stacks/prod/eks` });
    expect(within(table).getByText('null_resource.this["<script>alert(2)</script>"]')).toHaveAttribute('title', address);
    expect(resources().getByText(module)).toBeInTheDocument();
    const region = screen.getByRole('region', { name: 'Resources' });
    expect(region.querySelector('img, script')).toBeNull();
    expect([...region.querySelectorAll('[id]')].map((el) => el.id)).toEqual(['stack-resources', 'resource-filter']);
    for (const a of region.querySelectorAll('a[href]')) expect(a.getAttribute('href')).toMatch(/^(\/runs\/|https:\/\/)/);
  });
});
