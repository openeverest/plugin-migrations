import { useCallback, useEffect, useRef, useState } from 'react';
import { errorMessage } from '../utils/error.utils';

export interface PollingState<T> {
  data: T | undefined;
  error: string | undefined;
  loading: boolean;
  // Client time of the last successful load; unaffected by server clock skew.
  lastUpdatedAt: Date | undefined;
  refresh: () => void;
  setData: (data: T) => void;
}

// Loads immediately, then again `intervalMs` after each response, so slow
// responses never overlap. Changing `key` restarts the loop and clears data.
export function usePolling<T>(
  load: (signal: AbortSignal) => Promise<T>,
  intervalMs: number,
  key = '',
): PollingState<T> {
  const loadRef = useRef(load);
  loadRef.current = load;
  const [data, setData] = useState<T>();
  const [error, setError] = useState<string>();
  const [loading, setLoading] = useState(true);
  const [lastUpdatedAt, setLastUpdatedAt] = useState<Date>();
  const [generation, setGeneration] = useState(0);

  useEffect(() => {
    setData(undefined);
    setError(undefined);
    setLastUpdatedAt(undefined);
  }, [key]);

  useEffect(() => {
    let timer: ReturnType<typeof setTimeout> | undefined;
    let controller: AbortController | undefined;
    let active = true;

    const tick = async () => {
      controller = new AbortController();
      setLoading(true);
      try {
        const next = await loadRef.current(controller.signal);
        if (!active) return;
        setData(next);
        setError(undefined);
        setLastUpdatedAt(new Date());
      } catch (err) {
        if (!active || controller.signal.aborted) return;
        setError(errorMessage(err));
      }
      setLoading(false);
      timer = setTimeout(tick, intervalMs);
    };
    void tick();

    return () => {
      active = false;
      controller?.abort();
      if (timer !== undefined) clearTimeout(timer);
    };
  }, [intervalMs, key, generation]);

  const refresh = useCallback(() => setGeneration((value) => value + 1), []);

  return { data, error, loading, lastUpdatedAt, refresh, setData };
}
