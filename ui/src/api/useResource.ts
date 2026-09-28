import { useCallback, useEffect, useRef, useState } from 'preact/hooks';

/** The state of an asynchronously loaded value. */
export interface Resource<T> {
  data: T | undefined;
  error: Error | undefined;
  loading: boolean;
  reload: () => void;
}

interface State<T> {
  key: string;
  data?: T;
  error?: Error;
  loading: boolean;
}

function toError(err: unknown): Error {
  return err instanceof Error ? err : new Error(String(err));
}

/** Loads a value on mount and when deps change; reload keeps the previous data until the new value arrives. */
export function useResource<T>(load: (signal: AbortSignal) => Promise<T>, deps: readonly unknown[]): Resource<T> {
  const key = JSON.stringify(deps);
  const [tick, setTick] = useState(0);
  const [state, setState] = useState<State<T>>({ key, loading: true });
  const loadRef = useRef(load);
  loadRef.current = load;

  useEffect(() => {
    const ctrl = new AbortController();
    setState((prev) => (prev.key === key ? { ...prev, loading: true } : { key, loading: true }));
    loadRef.current(ctrl.signal).then(
      (data) => {
        if (!ctrl.signal.aborted) setState({ key, data, loading: false });
      },
      (err: unknown) => {
        if (ctrl.signal.aborted) return;
        setState((prev) => ({
          key,
          data: prev.key === key ? prev.data : undefined,
          error: toError(err),
          loading: false,
        }));
      },
    );
    return () => {
      ctrl.abort();
    };
  }, [key, tick]);

  const reload = useCallback(() => {
    setTick((t) => t + 1);
  }, []);

  const current = state.key === key ? state : { key, loading: true, data: undefined, error: undefined };
  return { data: current.data, error: current.error, loading: current.loading, reload };
}
