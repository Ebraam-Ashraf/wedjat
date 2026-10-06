import React, { useEffect, useState } from 'react';
import { staleAfterMs } from '../store/telemetryStore';

export function useFreshness(lastSampleAt, intervalMs) {
  const [now, setNow] = useState(() => performance.now());
  useEffect(() => {
    const timer = setInterval(() => setNow(performance.now()), 1000);
    return () => clearInterval(timer);
  }, []);
  const ageMs = lastSampleAt ? Math.max(0, now - lastSampleAt) : Infinity;
  const thresholdMs = staleAfterMs(intervalMs);
  return { ageMs, stale: ageMs > thresholdMs, thresholdMs };
}

export default function Freshness({ lastSampleAt, intervalMs, sequence, compact = false }) {
  const { ageMs, stale } = useFreshness(lastSampleAt, intervalMs);
  const age = Number.isFinite(ageMs) ? `${(ageMs / 1000).toFixed(1)} s ago` : 'waiting for sample';
  return <span className={`freshness ${stale ? 'freshness-stale' : 'freshness-live'} ${compact ? 'freshness-compact' : ''}`}>
    <i key={sequence ?? 'no-sample'} className="freshness-dot" />
    <span>{stale && lastSampleAt ? `stale · updated ${age}` : `updated ${age}`}</span>
  </span>;
}
