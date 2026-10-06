import { useCallback, useEffect, useRef, useState } from 'react';
import { apiGet } from '../api';

export function useApiData(path, { refreshIntervalMs = 0 } = {}) {
  const [state, setState] = useState({ data: null, loading: true, error: '', updatedAt: null });
  const requestId = useRef(0);

  const refresh = useCallback(async () => {
    const id = ++requestId.current;
    setState((current) => ({ ...current, loading: current.data == null, error: '' }));
    try {
      const data = await apiGet(path);
      if (id === requestId.current) setState({ data, loading: false, error: '', updatedAt: new Date() });
    } catch (error) {
      if (id === requestId.current) setState((current) => ({ ...current, loading: false, error: error.message }));
    }
  }, [path]);

  useEffect(() => {
    setState({ data: null, loading: true, error: '', updatedAt: null });
    refresh();
    if (!refreshIntervalMs) return () => { requestId.current += 1; };
    const timer = setInterval(refresh, refreshIntervalMs);
    return () => { clearInterval(timer); requestId.current += 1; };
  }, [path, refresh, refreshIntervalMs]);

  return { ...state, refresh };
}
