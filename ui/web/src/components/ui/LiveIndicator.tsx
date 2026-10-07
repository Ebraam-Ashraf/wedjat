import { useEffect, useRef } from 'react';

/** LiveIndicator — small pulsing dot for live / stale status. */
export interface LiveIndicatorProps {
  live?: boolean;
  label?: string;
  size?: number;
}

export default function LiveIndicator({ live = true, label, size = 12 }: LiveIndicatorProps) {
  const dotRef = useRef<HTMLSpanElement>(null);

  useEffect(() => {
    const el = dotRef.current;
    if (!el) return;
    el.style.setProperty('--live-size', `${size}px`);
    el.style.setProperty('--live-color', live ? 'var(--accent-success, #22c55e)' : 'var(--text-muted, #94a3b8)');
  }, [live, size]);

  return (
    <span
      ref={dotRef}
      className="live-indicator"
      style={{
        display: 'inline-block',
        width: 'var(--live-size)',
        height: 'var(--live-size)',
        borderRadius: 'var(--live-size)',
        background: 'var(--live-color)',
        boxShadow: live ? '0 0 8px var(--live-color)' : 'none',
        animation: live ? 'live-pulse 1.5s infinite' : 'none',
      }}
      aria-label={label ?? (live ? 'Live' : 'Stale')}
    />
  );
}
