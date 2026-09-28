import { createContext, type ComponentChildren } from 'preact';
import { useContext } from 'preact/hooks';

import { ApiClient } from './client';

const ApiContext = createContext<ApiClient>(new ApiClient());

/** Provides the API client to the tree; tests pass one with a fake fetch. */
export function ApiProvider({ client, children }: { client: ApiClient; children: ComponentChildren }) {
  return <ApiContext.Provider value={client}>{children}</ApiContext.Provider>;
}

/** Returns the API client of the nearest ApiProvider. */
export function useApi(): ApiClient {
  return useContext(ApiContext);
}
