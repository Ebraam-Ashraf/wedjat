import type { ReactNode } from 'react';

/** MetricCard — a single metric shown like terminal output. */
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

const TREND: Record<string, string> = { up: '▲', down: '▼', neutral: '-' };

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
      style={{ cursor: onClick ? 'pointer' : 'default' }}
    >
      <div className="term-metric-label">
        {icon} {label}
      </div>
      <div>
        <span className="term-metric-value">{value}</span>
        {unit && <span className="term-metric-unit">{unit}</span>}
      </div>
      {delta != null && (
        <small
          style={{
            color: trend === 'up' ? 'var(--ok)' : trend === 'down' ? 'var(--critical)' : 'var(--text-dim)',
          }}
        >
          {TREND[trend]} {delta}
        </small>
      )}
    </div>
  );
}
