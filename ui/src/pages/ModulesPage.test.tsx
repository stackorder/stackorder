import { fireEvent, screen, waitFor, within } from '@testing-library/preact';
import { describe, expect, it } from 'vitest';

import { moduleDetail } from '../fixtures';
import { nth, renderWithApp } from '../test/render';
import { consumersBehind, latestVersion, matchesModule, ModulesPage } from './ModulesPage';

describe('module helpers', () => {
  it('finds the latest version by tag time', () => {
    expect(latestVersion(moduleDetail)).toBe('v0.10.0');
    expect(latestVersion({})).toBeUndefined();
  });

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
