import type { ComponentChildren } from 'preact';
import { LocationProvider, Route, Router, useLocation } from 'preact-iso';
import { useState } from 'preact/hooks';

import { ApiClient, ApiError } from './api/client';
import { ApiProvider, useApi } from './api/context';
import type { Whoami } from './api/types';
import { useResource } from './api/useResource';
import { ErrorState, SignInLink } from './components/ErrorState';
import { Loading } from './components/Loading';
import { ModulePage } from './pages/ModulePage';
import { ModulesPage } from './pages/ModulesPage';
import { NotFoundPage } from './pages/NotFoundPage';
import { OverviewPage } from './pages/OverviewPage';
import { RepoGraphPage } from './pages/RepoGraphPage';
import { ReposPage } from './pages/ReposPage';
import { RunPage } from './pages/RunPage';
import { StackPage } from './pages/StackPage';
import { ROUTER_SCOPE } from './routerScope';

const NAV = [
  { href: '/', label: 'Overview', match: (p: string) => p === '/' },
  { href: '/repos', label: 'Repositories', match: (p: string) => p.startsWith('/repos') || p.startsWith('/stacks') },
  { href: '/modules', label: 'Modules', match: (p: string) => p.startsWith('/modules') },
];

function focusMain() {
  document.getElementById('main')?.focus({ preventScroll: true });
}

/** The page routes, rendered once the user is known to be signed in. */
export function Routes() {
  return (
    <Router onRouteChange={focusMain}>
      <Route path="/" component={OverviewPage} />
      <Route path="/repos" component={ReposPage} />
      <Route path="/repos/:owner/:repo" component={RepoGraphPage} />
      <Route path="/stacks/:id" component={StackPage} />
      <Route path="/runs/:id" component={RunPage} />
      <Route path="/modules" component={ModulesPage} />
      <Route path="/modules/:id" component={ModulePage} />
      <Route default component={NotFoundPage} />
    </Router>
  );
}

function Nav() {
  const { path } = useLocation();
  return (
    <nav class="app-nav" aria-label="Main">
      <ul>
        {NAV.map((item) => (
          <li key={item.href}>
            <a href={item.href} aria-current={item.match(path) ? 'page' : undefined}>
              {item.label}
            </a>
          </li>
        ))}
      </ul>
    </nav>
  );
}

function UserMenu({ user, onLogout }: { user: Whoami; onLogout: () => void }) {
  const api = useApi();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string>();
  const logout = async () => {
    setBusy(true);
    setError(undefined);
    try {
      await api.logout();
      onLogout();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
      setBusy(false);
    }
  };
  return (
    <div class="user-menu">
      {user.avatar_url && <img class="avatar" src={user.avatar_url} alt="" width={24} height={24} />}
      <span class="user-menu__login">{user.login}</span>
      <button type="button" class="button button--small" onClick={() => void logout()} disabled={busy}>
        Sign out
      </button>
      {error && (
        <span class="user-menu__error" role="alert">
          {error}
        </span>
      )}
    </div>
  );
}

function SignedOut() {
  return (
    <section class="signin">
      <h1>Sign in to Stackorder</h1>
      <p>Stackorder uses your GitHub account. You will see the repositories of the organisations where the App is installed.</p>
      <SignInLink />
    </section>
  );
}

function Frame({ header, children }: { header?: ComponentChildren; children: ComponentChildren }) {
  return (
    <>
      <a class="skip-link" href="#main">
        Skip to content
      </a>
      <header class="app-header">
        <a class="brand" href="/">
          <svg class="brand__mark" viewBox="0 0 32 32" width="22" height="22" aria-hidden="true">
            <rect x="3" y="4" width="12" height="7" rx="2" />
            <rect x="17" y="12.5" width="12" height="7" rx="2" />
            <rect x="3" y="21" width="12" height="7" rx="2" />
          </svg>
          Stackorder
        </a>
        {header}
      </header>
      <main id="main" tabindex={-1}>
        {children}
      </main>
    </>
  );
}

/** The application shell: header, navigation, sign-in gate and routes. */
export function Shell() {
  const api = useApi();
  const me = useResource((signal) => api.me({ signal }), []);
  const [signedOut, setSignedOut] = useState(false);

  const unauthorized = me.error instanceof ApiError && me.error.unauthorized;
  if (signedOut || unauthorized) {
    return (
      <Frame>
        <SignedOut />
      </Frame>
    );
  }
  if (me.error) {
    return (
      <Frame>
        <ErrorState error={me.error} onRetry={me.reload} />
      </Frame>
    );
  }
  if (!me.data) {
    return (
      <Frame>
        <Loading />
      </Frame>
    );
  }
  return (
    <Frame
      header={
        <>
          <Nav />
          <UserMenu
            user={me.data}
            onLogout={() => {
              setSignedOut(true);
            }}
          />
        </>
      }
    >
      <Routes />
    </Frame>
  );
}

/** The whole app, with an injectable API client. */
export function App({ client = new ApiClient() }: { client?: ApiClient }) {
  return (
    <ApiProvider client={client}>
      <LocationProvider scope={ROUTER_SCOPE}>
        <Shell />
      </LocationProvider>
    </ApiProvider>
  );
}
