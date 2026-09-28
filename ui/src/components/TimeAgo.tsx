const units: [Intl.RelativeTimeFormatUnit, number][] = [
  ['year', 365 * 24 * 3600],
  ['month', 30 * 24 * 3600],
  ['week', 7 * 24 * 3600],
  ['day', 24 * 3600],
  ['hour', 3600],
  ['minute', 60],
  ['second', 1],
];

const relative = new Intl.RelativeTimeFormat('en', { numeric: 'auto' });

/** Formats the distance between a time and now, such as "3 minutes ago". */
export function formatTimeAgo(value: Date, now: Date): string {
  const seconds = Math.round((value.getTime() - now.getTime()) / 1000);
  const abs = Math.abs(seconds);
  if (abs < 45) return 'just now';
  for (const [unit, size] of units) {
    if (abs >= size) return relative.format(Math.round(seconds / size), unit);
  }
  return 'just now';
}

/** Formats a duration in milliseconds as "4m 12s". */
export function formatDuration(ms: number): string {
  const total = Math.max(0, Math.round(ms / 1000));
  const h = Math.floor(total / 3600);
  const m = Math.floor((total % 3600) / 60);
  const s = total % 60;
  if (h > 0) return `${h}h ${m}m`;
  if (m > 0) return `${m}m ${s}s`;
  return `${s}s`;
}

/** A relative timestamp with the absolute time as its tooltip. */
export function TimeAgo({ value, now }: { value: string | undefined; now?: Date }) {
  if (!value) return <span class="muted">—</span>;
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return <span>{value}</span>;
  return (
    <time dateTime={value} title={date.toISOString().replace('T', ' ').replace(/\.\d+Z$/, ' UTC')}>
      {formatTimeAgo(date, now ?? new Date())}
    </time>
  );
}
