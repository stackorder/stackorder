import { render, screen } from '@testing-library/preact';
import { describe, expect, it } from 'vitest';

import { RUN_STATUSES, STACK_STATUSES } from '../api/types';
import { Badge, StatusBadge, statusTone, type AnyStatus, type Tone } from './StatusBadge';

describe('statusTone', () => {
  it.each<[AnyStatus, Tone]>([
    ['pending', 'neutral'],
    ['planning', 'running'],
    ['applying', 'running'],
    ['planned', 'ready'],
    ['applied', 'success'],
    ['failed', 'danger'],
    ['blocked', 'warning'],
    ['unconfirmed', 'warning'],
    ['unknown', 'warning'],
    ['noop', 'muted'],
    ['skipped', 'muted'],
    ['superseded', 'muted'],
    ['pass', 'success'],
    ['warn', 'warning'],
    ['fail', 'danger'],
  ])('%s is %s', (status, tone) => {
    expect(statusTone(status)).toBe(tone);
  });

  it('covers every run and stack status', () => {
    for (const s of [...RUN_STATUSES, ...STACK_STATUSES]) expect(statusTone(s)).toMatch(/^[a-z]+$/);
  });
});

describe('StatusBadge', () => {
  it.each<[AnyStatus, string, string]>([
    ['failed', 'failed', 'badge badge--danger'],
    ['applied', 'applied', 'badge badge--success'],
    ['blocked', 'blocked', 'badge badge--warning'],
    ['planning', 'planning', 'badge badge--running'],
  ])('renders %s as text and colour', (status, text, className) => {
    render(<StatusBadge status={status} />);
    const badge = screen.getByText(text);
    expect(badge).toHaveClass(...className.split(' '));
    expect(badge).toHaveAttribute('data-status', status);
  });

  it('appends extra classes', () => {
    render(<StatusBadge status="failed" class="badge--large" />);
    expect(screen.getByText('failed')).toHaveClass('badge', 'badge--danger', 'badge--large');
  });

  it('renders a tone badge with children', () => {
    render(<Badge tone="warning">drifted</Badge>);
    expect(screen.getByText('drifted')).toHaveClass('badge', 'badge--warning');
  });
});
