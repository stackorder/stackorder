import type { ComponentChildren } from 'preact';

import type { CheckStatus, RunStatus, StackStatus } from '../api/types';
import { humanize } from '../format';

/** Every status the badge knows how to colour. */
export type AnyStatus = RunStatus | StackStatus | CheckStatus;

/** Colour family of a status; each tone has a CSS variable pair. */
export type Tone = 'neutral' | 'running' | 'ready' | 'success' | 'danger' | 'warning' | 'muted';

/** Maps a run, stack or check status to its tone. */
export function statusTone(status: AnyStatus): Tone {
  switch (status) {
    case 'planning':
    case 'applying':
      return 'running';
    case 'planned':
      return 'ready';
    case 'applied':
    case 'pass':
      return 'success';
    case 'failed':
    case 'fail':
      return 'danger';
    case 'blocked':
    case 'unconfirmed':
    case 'unknown':
    case 'warn':
      return 'warning';
    case 'noop':
    case 'skipped':
    case 'superseded':
      return 'muted';
    case 'pending':
      return 'neutral';
  }
}

/** A pill showing a status in words and colour. */
export function StatusBadge({ status, class: className }: { status: AnyStatus; class?: string }) {
  const tone = statusTone(status);
  return (
    <span class={`badge badge--${tone}${className ? ` ${className}` : ''}`} data-status={status}>
      {humanize(status)}
    </span>
  );
}

/** A pill with an explicit tone, for states that are not run or stack statuses. */
export function Badge({ tone, children }: { tone: Tone; children: ComponentChildren }) {
  return <span class={`badge badge--${tone}`}>{children}</span>;
}
