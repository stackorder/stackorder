import { fireEvent, screen, waitFor, within } from '@testing-library/preact';
import { describe, expect, it } from 'vitest';

import { Routes } from '../app';
import { ids } from '../fixtures';
import { json, renderWithApp } from '../test/render';
import { RepoGraphPage } from './RepoGraphPage';

const page = <RepoGraphPage owner="acme" repo="infra" />;

describe('RepoGraphPage', () => {
  it('renders the current graph with a legend, a run selector and the runs table', async () => {
    const { calls } = renderWithApp(page, { url: '/repos/acme/infra' });
    const graph = await screen.findByRole('group', { name: 'Dependency graph of acme/infra' });
    expect(within(graph).getAllByRole('link')).toHaveLength(8);
    expect(screen.getByRole('heading', { level: 1, name: 'acme/infra' })).toBeInTheDocument();
    expect(screen.getByText('4f9c2d1')).toBeInTheDocument();
    expect(screen.getByRole('heading', { name: 'Legend' })).toBeInTheDocument();
    expect(screen.queryByRole('heading', { name: 'Affected set' })).not.toBeInTheDocument();

    const select = screen.getByLabelText('Replay run');
    await waitFor(() => {
      expect(within(select).getAllByRole('option')).toHaveLength(6);
    });
    expect(within(select).getAllByRole('option')[1]?.textContent).toMatch(/^7d9f1b3c · apply · PR #42 · failed · /);
    expect(await screen.findByRole('table', { name: 'Runs of acme/infra' })).toBeInTheDocument();
    expect(screen.getByRole('table', { name: 'Edges of acme/infra' })).toBeInTheDocument();
    expect(calls.find((c) => c.path === '/v1/repos/acme/infra/graph')?.search).toBe('');
    expect(calls.some((c) => c.path.startsWith('/v1/runs/'))).toBe(false);
  });

  it('replays a run: affected set, waves and statuses', async () => {
    const { calls } = renderWithApp(page, { url: `/repos/acme/infra?run=${ids.run}` });
    const panel = await screen.findByRole('region', { name: 'Affected set' });
    await within(panel).findByText('7d9f1b3c');
    expect(within(panel).getByText('5 affected stacks in 3 waves.')).toBeInTheDocument();
    const waves = within(panel).getAllByRole('heading', { level: 3 }).map((h) => h.textContent);
    expect(waves).toEqual(['Wave 0', 'Wave 1', 'Wave 2']);
    expect(within(panel).getByRole('link', { name: 'stacks/prod/apps' })).toHaveAttribute(
      'href',
      `/stacks/${ids.stacks['stacks/prod/apps'] ?? ''}`,
    );
    const graph = screen.getByRole('group', { name: 'Dependency graph of acme/infra' });
    expect(
      await within(graph).findByRole('link', { name: 'Stack stacks/prod/eks, status failed, wave 1, affected because dependent' }),
    ).toHaveClass('node--tone-danger');
    expect(within(graph).getByRole('link', { name: /^Module acme\/infra\/\/modules\/eks$/ })).toHaveClass('node--dimmed');
    expect(screen.getByLabelText('Replay run')).toHaveValue(ids.run);
    expect(calls.find((c) => c.path === '/v1/repos/acme/infra/graph')?.search).toBe(`?run=${ids.run}`);
    expect(calls.some((c) => c.path === `/v1/runs/${ids.run}`)).toBe(true);
  });

  it('keeps the replay when the run detail cannot be loaded', async () => {
    renderWithApp(page, {
      url: `/repos/acme/infra?run=${ids.run}`,
      handler: (c) => (c.path.startsWith('/v1/runs/') ? json({ code: 'forbidden', message: 'no access' }, 403) : undefined),
    });
    expect(await screen.findByText('Stack statuses are unavailable: no access')).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Stack stacks/prod/eks, wave 1, affected because dependent' })).toBeInTheDocument();
  });

  it('selecting a run puts it in the URL and fetches its replay', async () => {
    const { calls } = renderWithApp(page, { url: '/repos/acme/infra?ref=main' });
    const select = await screen.findByLabelText('Replay run');
    await waitFor(() => {
      expect(within(select).getAllByRole('option')).toHaveLength(6);
    });
    fireEvent.change(select, { target: { value: ids.planRun } });
    await waitFor(() => {
      expect(window.location.search).toBe(`?ref=main&run=${ids.planRun}`);
    });
    await screen.findByRole('region', { name: 'Affected set' });
    expect(calls.some((c) => c.path === '/v1/repos/acme/infra/graph' && c.search === `?ref=main&run=${ids.planRun}`)).toBe(true);
  });

  it('submitting a ref reloads the graph at that ref', async () => {
    const { calls } = renderWithApp(page, { url: '/repos/acme/infra' });
    const input = await screen.findByLabelText('Ref');
    fireEvent.input(input, { target: { value: ' release/2026-09 ' } });
    fireEvent.submit(input);
    await waitFor(() => {
      expect(window.location.search).toBe('?ref=release%2F2026-09');
    });
    await waitFor(() => {
      expect(calls.some((c) => c.search === '?ref=release%2F2026-09')).toBe(true);
    });
  });

  it('shows the ref of the current address after going back', async () => {
    renderWithApp(<Routes />, { url: '/repos/acme/infra' });
    const input = await screen.findByLabelText('Ref');
    fireEvent.input(input, { target: { value: 'release/2026-09' } });
    fireEvent.submit(input);
    await waitFor(() => {
      expect(window.location.search).toBe('?ref=release%2F2026-09');
    });
    window.history.back();
    await waitFor(() => {
      expect(window.location.search).toBe('');
    });
    await waitFor(() => {
      expect(screen.getByLabelText('Ref')).toHaveValue('');
    });
  });

  it('navigates to a stack page when a stack node is activated', async () => {
    renderWithApp(page, { url: '/repos/acme/infra' });
    const node = await screen.findByRole('link', { name: 'Stack stacks/prod/eks' });
    fireEvent.keyDown(node, { key: 'Enter' });
    await waitFor(() => {
      expect(window.location.pathname).toBe(`/stacks/${ids.stack}`);
    });
  });

  it('navigates to the module list filtered by the module when a module node is clicked', async () => {
    renderWithApp(page, { url: '/repos/acme/infra' });
    fireEvent.click(await screen.findByRole('link', { name: 'Module acme/terraform-modules//eks-addons@v0.8.0' }));
    await waitFor(() => {
      expect(window.location.pathname + window.location.search).toBe(
        `/modules?q=${encodeURIComponent('acme/terraform-modules//eks-addons')}`,
      );
    });
  });

  it('shows a not-found state for an unknown repository', async () => {
    renderWithApp(<RepoGraphPage owner="acme" repo="nope" />, { url: '/repos/acme/nope' });
    expect(await screen.findByRole('heading', { name: 'Not found' })).toBeInTheDocument();
    expect(screen.getByText('This repository does not exist or you do not have access to it.')).toBeInTheDocument();
  });
});
