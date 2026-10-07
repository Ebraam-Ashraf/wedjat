import { useEffect, useRef, useState } from 'react';
import type { TelemetryStoreLike } from '../types';

// Subscribe a component to one store slice, re-rendering only when it matters.
//
// Two independent guards keep React out of the hot path:
//
//   1. The store only notifies when a slice's value signature actually changed,
//      so an idle GPU that reports 0% and 356MB twice a second notifies zero
//      times. This is the guard that does the real work.
//
//   2. A minimum interval between commits, so even genuinely churning data
//      cannot drive more than ~2 renders per second.
//
// The store keeps a stable array reference when nothing changed, so plain
// reference equality in setState is sufficient to drop the update.

export function useStoreValue<T>(
  store: TelemetryStoreLike,
  slice: string,
  select: (store: TelemetryStoreLike) => T,
  minIntervalMs: number = 500,
): T {
  const [value, setValue] = useState<T>(() => select(store));

  // Keep the latest selector without making it an effect dependency; a new
  // inline arrow on every render must not tear down the subscription.
  const selectRef = useRef(select);
  selectRef.current = select;

  useEffect(() => {
    let timer: ReturnType<typeof setTimeout> | 0 = 0;
    let lastCommit = 0;
    let cancelled = false;

    const flush = () => {
      timer = 0;
      if (cancelled) return;
      lastCommit = performance.now();
      const next = selectRef.current(store);
      // Identity check: the store hands back the same reference when the slice
      // is unchanged, so this is the whole change detection.
      setValue((prev) => (Object.is(prev, next) ? prev : next));
    };

    const onNotify = () => {
      const wait = minIntervalMs - (performance.now() - lastCommit);
      if (wait <= 0) {
        flush();
      } else if (!timer) {
        // Trailing edge, so the newest value always lands even when notifications
        // arrive faster than the commit floor allows.
        timer = setTimeout(flush, wait);
      }
    };

    // Pick up anything that changed between first render and subscription.
    flush();

    const unsubscribe = store.subscribe(slice, onNotify);
    return () => {
      cancelled = true;
      if (timer) clearTimeout(timer);
      unsubscribe();
    };
  }, [store, slice, minIntervalMs]);

  return value;
}

export default useStoreValue;
