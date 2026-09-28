import { screen, within } from '@testing-library/preact';
import { describe, expect, it } from 'vitest';

import { json, nth, renderWithApp } from '../test/render';
import { ReposPage } from './ReposPage';

describe('ReposPage', () => {
  it('lists repositories with counts and links to their graphs', async () => {
    renderWithApp(<ReposPage />);
    const table = await screen.findByRole('table', { name: 'Repositories' });
    const rows = within(table).getAllByRole('row').slice(1);
    expect(rows.map((r) => within(r).getByRole('link').getAttribute('href'))).toEqual([
      '/repos/acme/infra',
      '/repos/acme/network-infra',
      '/repos/acme/platform-infra',
    ]);
    const infra = nth(rows, 0);
    expect([...infra.querySelectorAll('td')].map((td) => td.textContent).slice(0, 5)).toEqual([
      'acme/infra',
      'main',
      '5',
      '1',
      '5',
    ]);
    expect(within(infra).getByText('1')).toHaveClass('text-warning');
    expect(within(nth(rows, 2)).getByText('trunk')).toBeInTheDocument();
    expect(within(nth(rows, 2)).getByText('—')).toBeInTheDocument();
  });

  it('explains how to get a first repository', async () => {
    renderWithApp(<ReposPage />, { handler: (c) => (c.path === '/v1/repos' ? json({ items: null }) : undefined) });
    expect(await screen.findByText(/Install the GitHub App/)).toBeInTheDocument();
  });
});
