import type { ReactNode } from 'react';

/** MetricCard — a single large metric with a label and optional trend/sparkline. */
export interface MetricCardProps {
  label: string;
  value: string | number;
  unit?: string;
  delta?: string;
  trend?: 'up' | 'down' | 'neutral';
  icon?: ReactNode;
  onClick?: () => void;
  className?: string;
}

const TREND_EMOJI: Record<string, string> = { up: '▲', down: '▼', neutral: '—' };

export default function MetricCard({
  label,
  value,
  unit,
  delta,
  trend = 'neutral',
  icon,
  onClick,
  className = '',
}: MetricCardProps) {
  return (
    <div
      className={`glass-panel ${className}`}
      onClick={onClick}
      style={{
        padding: 16,
        borderRadius: 12,
        cursor: onClick ? 'pointer' : 'default',
      }}
    >
      <div style={{ display: 'flex', alignItems: 'flex-start', gap: 12, marginBottom: 8 }}>
        {icon}
        <span style={{ color: 'var(--text-dim)', fontSize: '0.85rem', fontWeight: 500 }}>{label}</span>
      </div>
      <div style={{ display: 'flex', alignItems: 'baseline', gap: 8 }}>
        <span style={{ fontSize: '1.75rem', fontWeight: 700, fontFamily: 'monospace' }}>{value}</span>
        {unit && <span style={{ color: 'var(--text-dim)', fontSize: '1.25rem' }}>{unit}</span>}
      </div>
      {delta != null && (
        <small style={{ color: trend === 'up' ? 'var(--ok)' : trend === 'down' ? 'var(--critical)' : 'var(--text-dim)' }}>
          {TREND_EMOJI[trend]} {delta}
        </small>
      )}
    </div>
  );
}
