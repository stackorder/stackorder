import { screen, within } from '@testing-library/preact';
import { describe, expect, it } from 'vitest';

import { clone, ids, moduleDetail } from '../fixtures';
import { json, nth, renderWithApp } from '../test/render';
import { ModulePage } from './ModulePage';

describe('ModulePage', () => {
  it('shows the versions newest first and consumers with their lag', async () => {
    renderWithApp(<ModulePage id={ids.module} />, { url: `/modules/${ids.module}` });
    expect(await screen.findByRole('heading', { level: 1, name: 'acme/terraform-modules//eks-addons' })).toBeInTheDocument();
    expect(screen.getByText('git')).toBeInTheDocument();

    const versions = screen.getByRole('table', { name: 'Versions of acme/terraform-modules//eks-addons' });
    expect(within(versions).getAllByRole('row').slice(1).map((r) => r.querySelector('td')?.textContent)).toEqual([
      'v0.10.0',
      'v0.9.0',
      'v0.8.0',
      'v0.7.2',
    ]);
    expect(within(versions).getByText('e3b0c44')).toBeInTheDocument();

    const consumers = screen.getByRole('table', { name: 'Stacks that use acme/terraform-modules//eks-addons' });
    const rows = within(consumers).getAllByRole('row').slice(1);
    expect(rows.map((r) => [...r.querySelectorAll('td')].map((td) => td.textContent))).toEqual([
      ['stacks/prod/eks', 'acme/infra', 'v0.8.0', '2 versions behind'],
      ['stacks/staging/eks', 'acme/infra', 'v0.8.0', '2 versions behind'],
      ['stacks/shared/eks', 'acme/platform-infra', 'v0.10.0', 'up to date'],
    ]);
    expect(rows[0]).toHaveClass('row--warning');
    expect(rows[2]).not.toHaveClass('row--warning');
    expect(within(nth(rows, 0)).getByRole('link', { name: 'stacks/prod/eks' })).toHaveAttribute(
      'href',
      `/stacks/${ids.stack}`,
    );
    expect(within(nth(rows, 2)).getByRole('link', { name: 'acme/platform-infra' })).toHaveAttribute(
      'href',
      '/repos/acme/platform-infra',
    );
  });

  it('names the latest version the server picks, not the last one tagged', async () => {
    const m = clone(moduleDetail);
    m.latest = 'v1.4.1';
    m.versions = [
      { version: 'v1.2.0', tagged_at: '2026-08-01T00:00:00Z' },
      { version: 'v1.4.1', tagged_at: '2026-08-10T00:00:00Z' },
      { version: 'v2.0.0-rc.1', tagged_at: '2026-08-20T00:00:00Z' },
      { version: 'v1.3.0', tagged_at: '2026-09-01T00:00:00Z' },
    ];
    renderWithApp(<ModulePage id={ids.module} />, {
      url: `/modules/${ids.module}`,
      handler: (c) => (c.path === `/v1/modules/${ids.module}` ? json(m) : undefined),
    });
    await screen.findByRole('heading', { level: 1 });
    expect(screen.getByText('Latest').nextElementSibling).toHaveTextContent('v1.4.1');
  });

  it('explains missing versions for local modules', async () => {
    renderWithApp(<ModulePage id="local" />, {
      handler: (c) =>
        c.path === '/v1/modules/local'
          ? json({ id: 'local', key: 'acme/infra//modules/vpc', kind: 'local', source: '../../../modules/vpc' })
          : undefined,
    });
    expect(await screen.findByText('Local modules have no tracked versions.')).toBeInTheDocument();
    expect(screen.getByText('No stack uses this module.')).toBeInTheDocument();
  });

  it('shows not found for an unknown module', async () => {
    renderWithApp(<ModulePage id="missing" />);
    expect(await screen.findByText('This module does not exist or you do not have access to it.')).toBeInTheDocument();
  });
});
