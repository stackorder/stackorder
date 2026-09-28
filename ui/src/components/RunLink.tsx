import type { Run } from '../api/types';
import { humanize, shortId } from '../format';

/** Short description of what a run is, such as "apply · PR #42". */
export function runTitle(run: Pick<Run, 'mode' | 'pr_number' | 'trigger'>): string {
  const what = run.pr_number ? `PR #${String(run.pr_number)}` : humanize(run.trigger);
  return `${run.mode} · ${what}`;
}

/** A link to a run page labelled with the run's short id. */
export function RunLink({ id }: { id: string }) {
  return (
    <a href={`/runs/${encodeURIComponent(id)}`} class="mono" title={id}>
      {shortId(id)}
    </a>
  );
}
