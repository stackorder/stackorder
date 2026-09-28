import { fireEvent, render, screen, waitFor, within } from '@testing-library/preact';
import { describe, expect, it } from 'vitest';

import { App } from './app';
import { ApiClient } from './api/client';
import { ids } from './fixtures';
import { ROUTER_SCOPE } from './routerScope';
import { fakeApi, json, type Handler } from './test/render';

function renderApp(url: string, handler?: Handler) {
  window.history.replaceState(null, '', url);
  const api = fakeApi(handler);
  render(<App client={new ApiClient({ fetch: api.fetch })} />);
  return api.calls;
}

const signedOut: Handler = (c) => (c.path === '/v1/me' ? json({ code: 'unauthorized', message: 'no session' }, 401) : undefined);

describe('ROUTER_SCOPE', () => {
  it.each([
    ['/', true],
    ['/repos/acme/infra', true],
    ['/runs/abc?x=1', true],
    ['/authors', true],
    ['/auth/login', false],
    ['/auth', false],
    ['/v1/me', false],
    ['/v1', false],
  ])('%s handled by the router: %s', (href, want) => {
    expect(ROUTER_SCOPE.test(href)).toBe(want);
  });
});

describe('App', () => {
  it('asks a signed-out user to sign in and loads no data', async () => {
    const calls = renderApp('/runs/whatever', signedOut);
    expect(await screen.findByRole('heading', { name: 'Sign in to Stackorder' })).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Sign in with GitHub' })).toHaveAttribute('href', '/auth/login');
    expect(screen.queryByRole('navigation', { name: 'Main' })).not.toBeInTheDocument();
    expect(calls.map((c) => c.path)).toEqual(['/v1/me']);
  });

  it('shows the user, the navigation and the overview when signed in', async () => {
    renderApp('/');
    expect(await screen.findByRole('heading', { level: 1, name: 'Overview' })).toBeInTheDocument();
    const nav = within(screen.getByRole('navigation', { name: 'Main' }));
    expect(nav.getByRole('link', { name: 'Overview' })).toHaveAttribute('aria-current', 'page');
    expect(nav.getByRole('link', { name: 'Repositories' })).not.toHaveAttribute('aria-current');
    expect(screen.getByText('octocat', { selector: '.user-menu__login' })).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Skip to content' })).toHaveAttribute('href', '#main');
  });

  it('routes between pages from the navigation', async () => {
    renderApp('/');
    await screen.findByRole('heading', { level: 1, name: 'Overview' });
    fireEvent.click(screen.getByRole('link', { name: 'Repositories' }));
    expect(await screen.findByRole('heading', { level: 1, name: 'Repositories' })).toBeInTheDocument();
    expect(window.location.pathname).toBe('/repos');
    expect(screen.getByRole('link', { name: 'Repositories' })).toHaveAttribute('aria-current', 'page');
    fireEvent.click(await screen.findByRole('link', { name: 'acme/infra' }));
    expect(await screen.findByRole('group', { name: 'Dependency graph of acme/infra' })).toBeInTheDocument();
    fireEvent.click(screen.getByRole('link', { name: 'Stack stacks/prod/eks' }));
    expect(await screen.findByRole('heading', { level: 1, name: 'stacks/prod/eks' })).toBeInTheDocument();
    expect(window.location.pathname).toBe(`/stacks/${ids.stack}`);
  });

  it.each([
    [`/runs/${ids.run}`, 'Run 7d9f1b3c failed'],
    [`/modules/${ids.module}`, 'acme/terraform-modules//eks-addons'],
    ['/modules', 'Modules'],
    ['/nowhere/at/all', 'Page not found'],
  ])('renders %s', async (url, heading) => {
    renderApp(url);
    expect(await screen.findByRole('heading', { level: 1, name: heading })).toBeInTheDocument();
  });

  it('signs out through the API', async () => {
    const calls = renderApp('/', (c) => (c.path === '/auth/logout' ? new Response(null, { status: 204 }) : undefined));
    fireEvent.click(await screen.findByRole('button', { name: 'Sign out' }));
    expect(await screen.findByRole('heading', { name: 'Sign in to Stackorder' })).toBeInTheDocument();
    expect(calls.find((c) => c.path === '/auth/logout')?.method).toBe('POST');
  });

  it('keeps the session when signing out fails', async () => {
    renderApp('/', (c) => (c.path === '/auth/logout' ? json({ code: 'internal', message: 'try later' }, 500) : undefined));
    fireEvent.click(await screen.findByRole('button', { name: 'Sign out' }));
    expect(await screen.findByRole('alert')).toHaveTextContent('try later');
    expect(screen.getByRole('button', { name: 'Sign out' })).toBeEnabled();
  });

  it('offers a retry when the server cannot say who the user is', async () => {
    let down = true;
    renderApp('/', (c) => (c.path === '/v1/me' && down ? json({ code: 'internal', message: 'database unavailable' }, 500) : undefined));
    expect(await screen.findByText('database unavailable')).toBeInTheDocument();
    down = false;
    fireEvent.click(screen.getByRole('button', { name: 'Try again' }));
    await waitFor(() => {
      expect(screen.getByText('octocat', { selector: '.user-menu__login' })).toBeInTheDocument();
    });
  });
});
