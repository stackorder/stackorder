import { ApiError, NETWORK_ERROR } from '../api/client';

/** Path that starts the GitHub sign-in flow. */
export const LOGIN_PATH = '/auth/login';

/** Link that starts the GitHub sign-in flow. */
export function SignInLink() {
  return (
    <a class="button button--primary" href={LOGIN_PATH}>
      Sign in with GitHub
    </a>
  );
}

/** Explains a failed load, with sign-in for 401 and a retry for anything transient. */
export function ErrorState({ error, onRetry, what }: { error: Error; onRetry?: () => void; what?: string }) {
  const api = error instanceof ApiError ? error : undefined;
  if (api?.unauthorized) {
    return (
      <section class="state state--error" role="alert">
        <h2>Signed out</h2>
        <p>Your session has ended. Sign in again to continue.</p>
        <SignInLink />
      </section>
    );
  }
  if (api?.notFound) {
    return (
      <section class="state state--error" role="alert">
        <h2>Not found</h2>
        <p>{what ? `This ${what} does not exist or you do not have access to it.` : api.message}</p>
        <a href="/">Back to the overview</a>
      </section>
    );
  }
  const heading = api?.code === NETWORK_ERROR ? 'The server is unreachable' : 'Something went wrong';
  return (
    <section class="state state--error" role="alert">
      <h2>{heading}</h2>
      <p>{error.message}</p>
      {onRetry && (
        <button type="button" class="button" onClick={onRetry}>
          Try again
        </button>
      )}
    </section>
  );
}
