import { fireEvent, screen, waitFor, within } from '@testing-library/preact';
import { describe, expect, it } from 'vitest';

import { clone, moduleDetail } from '../fixtures';
import { json, nth, paged, renderWithApp } from '../test/render';
import { consumersBehind, matchesModule, ModulesPage } from './ModulesPage';

describe('module helpers', () => {
  it('counts consumers behind', () => {
    expect(consumersBehind(moduleDetail)).toBe(2);
    expect(consumersBehind({})).toBe(0);
  });

  it.each([
    ['', true],
    ['EKS-ADDONS', true],
    ['github.com/acme/terraform-modules', true],
    ['acme/terraform-modules//eks-addons', true],
    ['vpc', false],
  ])('filter %j matches: %s', (q, want) => {
    expect(matchesModule(moduleDetail, q)).toBe(want);
  });
});

describe('ModulesPage', () => {
  it('lists modules with latest versions, consumers and lag', async () => {
    renderWithApp(<ModulesPage />, { url: '/modules' });
    const table = await screen.findByRole('table', { name: 'Modules' });
    const rows = within(table).getAllByRole('row').slice(1);
    expect(rows.map((r) => [...r.querySelectorAll('td')].map((td) => td.textContent))).toEqual([
      ['acme/infra//modules/vpc', 'local', '../../../modules/vpc', '—', '2', '—'],
      ['acme/infra//modules/eks', 'local', '../../../modules/eks', '—', '2', '—'],
      [
        'acme/terraform-modules//eks-addons',
        'git',
        'git::https://github.com/acme/terraform-modules.git//eks-addons',
        'v0.10.0',
        '3',
        '2 behind',
      ],
      ['registry:terraform-aws-modules/iam/aws@5.44', 'registry', 'terraform-aws-modules/iam/aws', '—', '1', '—'],
    ]);
    expect(within(nth(rows, 2)).getByRole('link')).toHaveAttribute('href', `/modules/${moduleDetail.id}`);
    expect(screen.getByText('4 of 4')).toBeInTheDocument();
  });

  it('shows the latest version the server picks, not the last one tagged', async () => {
    const m = clone(moduleDetail);
    m.latest = 'v1.4.1';
    m.versions = [
      { version: 'v1.3.0', tagged_at: '2026-09-20T00:00:00Z' },
      { version: 'v2.0.0-rc.1', tagged_at: '2026-09-10T00:00:00Z' },
      { version: 'v1.4.1', tagged_at: '2026-09-01T00:00:00Z' },
    ];
    renderWithApp(<ModulesPage />, { url: '/modules', handler: (c) => (c.path === '/v1/modules' ? json({ items: [m] }) : undefined) });
    const table = await screen.findByRole('table', { name: 'Modules' });
    expect(nth(within(table).getAllByRole('row'), 1).querySelectorAll('td')[3]?.textContent).toBe('v1.4.1');
  });

  it('finds a module past the first page from a ?q= link', async () => {
    const many = Array.from({ length: 70 }, (_, i) => ({
      id: `m${String(i)}`,
      key: `acme/infra//modules/m${String(i).padStart(2, '0')}`,
      kind: 'local',
      source: `../../modules/m${String(i).padStart(2, '0')}`,
    }));
    renderWithApp(<ModulesPage />, {
      url: `/modules?q=${encodeURIComponent('acme/infra//modules/m60')}`,
      handler: (c) => (c.path === '/v1/modules' ? paged(many, c) : undefined),
    });
    const table = await screen.findByRole('table', { name: 'Modules' });
    expect(within(table).getByRole('link', { name: 'acme/infra//modules/m60' })).toHaveAttribute('href', '/modules/m60');
    expect(screen.getByText('1 of 70')).toBeInTheDocument();
  });

  it('filters from the URL and as the user types', async () => {
    renderWithApp(<ModulesPage />, { url: `/modules?q=${encodeURIComponent('acme/terraform-modules//eks-addons')}` });
    const table = await screen.findByRole('table', { name: 'Modules' });
    expect(within(table).getAllByRole('row')).toHaveLength(2);
    expect(screen.getByText('1 of 4')).toBeInTheDocument();

    fireEvent.input(screen.getByLabelText('Filter'), { target: { value: 'infra//' } });
    await waitFor(() => {
      expect(window.location.search).toBe('?q=infra%2F%2F');
    });
    expect(await screen.findByText('2 of 4')).toBeInTheDocument();

    fireEvent.input(screen.getByLabelText('Filter'), { target: { value: 'nothing-matches' } });
    expect(await screen.findByText('No module matches the filter.')).toBeInTheDocument();

    fireEvent.input(screen.getByLabelText('Filter'), { target: { value: '' } });
    await waitFor(() => {
      expect(window.location.search).toBe('');
    });
    expect(await screen.findByText('4 of 4')).toBeInTheDocument();
  });
});
