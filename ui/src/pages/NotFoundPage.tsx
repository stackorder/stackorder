import { usePageTitle } from '../usePageTitle';

/** Shown for any path the router does not know. */
export function NotFoundPage() {
  usePageTitle('Not found');
  return (
    <div class="page">
      <h1>Page not found</h1>
      <p>
        There is nothing at this address. <a href="/">Go to the overview</a>.
      </p>
    </div>
  );
}
