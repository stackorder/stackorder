import { fireEvent, screen, waitFor, within } from '@testing-library/preact';
import { describe, expect, it } from 'vitest';

import type { StackDetail } from '../api/types';
import { Routes } from '../app';
import { clone, ids, stack } from '../fixtures';
import { type Handler, json, nth, paged, renderWithApp } from '../test/render';
import { StackPage } from './StackPage';

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

  it('shows not found for an unknown stack', async () => {
    renderWithApp(<StackPage id="00000000-0000-4000-8000-000000000000" />, { url: '/stacks/00000000-0000-4000-8000-000000000000' });
    expect(await screen.findByText('This stack does not exist or you do not have access to it.')).toBeInTheDocument();
  });
});
