/** A polite live region shown while data loads. */
export function Loading({ label = 'Loading…' }: { label?: string }) {
  return (
    <div class="state state--loading" role="status" aria-live="polite">
      <span class="spinner" aria-hidden="true" />
      {label}
    </div>
  );
}
