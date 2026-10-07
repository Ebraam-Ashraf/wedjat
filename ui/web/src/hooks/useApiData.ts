import { useCallback, useEffect, useRef, useState } from 'react';
import type { ApiDataResult } from '../types';
import { apiGet } from '../api';

export function useApiData<T = unknown>(
  path: string,
  { refreshIntervalMs = 0 }: { refreshIntervalMs?: number } = {},
): ApiDataResult<T> {
  const [state, setState] = useState<ApiDataResult<T>>({
    data: null, loading: true, error: '', updatedAt: null,
    refresh: async () => {},
  });
  const requestId = useRef(0);

  const refresh = useCallback(async () => {
    const id = ++requestId.current;
    setState((current) => ({ ...current, loading: current.data == null, error: '' }));
    try {
      const data = await apiGet<T>(path);
      if (id === requestId.current) {
        setState((current) => ({ ...current, data, loading: false, error: '', updatedAt: new Date() }));
      }
    } catch (error) {
      if (id === requestId.current) {
        setState((current) => ({ ...current, loading: false, error: (error as Error).message }));
      }
    }
  }, [path]);

  useEffect(() => {
    setState((s) => ({ ...s, refresh }));
    void refresh();
    if (!refreshIntervalMs) return () => { requestId.current += 1; };
    const timer = setInterval(refresh, refreshIntervalMs);
    return () => { clearInterval(timer); requestId.current += 1; };
  }, [path, refresh, refreshIntervalMs]);

  return state;
}
