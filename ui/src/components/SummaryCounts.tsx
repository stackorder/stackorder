import type { PlanSummary } from '../api/types';
import { plural, summaryTotal } from '../format';

/** Plain-language description of a plan summary, used as the accessible text. */
export function describeSummary(s: PlanSummary): string {
  const parts = [`${s.adds} to add`, `${s.changes} to change`, `${s.destroys} to destroy`];
  if (s.replaces > 0) parts.push(`${s.replaces} to replace`);
  if (s.imports) parts.push(`${s.imports} to import`);
  if (s.moves) parts.push(`${s.moves} to move`);
  if (s.output_changes) parts.push(plural(s.output_changes, 'output change'));
  return parts.join(', ');
}

/** Terraform-style +adds ~changes -destroys counts for a plan summary. */
export function SummaryCounts({ summary }: { summary: PlanSummary | undefined }) {
  if (!summary) {
    return (
      <span class="counts counts--none">
        <span aria-hidden="true">—</span>
        <span class="sr-only">No plan summary</span>
      </span>
    );
  }
  if (summaryTotal(summary) === 0 && !summary.output_changes) {
    return <span class="counts counts--empty">No changes</span>;
  }
  return (
    <span class="counts">
      <span aria-hidden="true" class="counts__visual">
        <span class="count count--add">+{summary.adds}</span>
        <span class="count count--change">~{summary.changes}</span>
        <span class="count count--destroy">-{summary.destroys}</span>
        {summary.replaces > 0 && <span class="count count--replace">±{summary.replaces}</span>}
        {summary.imports ? <span class="count count--other">{plural(summary.imports, 'import')}</span> : null}
        {summary.moves ? <span class="count count--other">{plural(summary.moves, 'move')}</span> : null}
        {summary.output_changes && summaryTotal(summary) === 0 ? (
          <span class="count count--other">{plural(summary.output_changes, 'output')}</span>
        ) : null}
      </span>
      <span class="sr-only">{describeSummary(summary)}</span>
    </span>
  );
}
