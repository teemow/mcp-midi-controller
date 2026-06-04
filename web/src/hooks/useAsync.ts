import { useCallback, useState } from "react";

interface AsyncState<T> {
  data: T | null;
  error: string | null;
  loading: boolean;
}

// useAsync wraps an async action with loading/error/data state and a stable
// run() that captures the latest result. Tabs use it for fetches and calls.
export function useAsync<Args extends unknown[], T>(
  fn: (...args: Args) => Promise<T>,
) {
  const [state, setState] = useState<AsyncState<T>>({
    data: null,
    error: null,
    loading: false,
  });

  const run = useCallback(
    async (...args: Args): Promise<T | undefined> => {
      setState((s) => ({ ...s, loading: true, error: null }));
      try {
        const data = await fn(...args);
        setState({ data, error: null, loading: false });
        return data;
      } catch (e) {
        setState({ data: null, error: e instanceof Error ? e.message : String(e), loading: false });
        return undefined;
      }
    },
    [fn],
  );

  const reset = useCallback(() => setState({ data: null, error: null, loading: false }), []);

  return { ...state, run, reset };
}
