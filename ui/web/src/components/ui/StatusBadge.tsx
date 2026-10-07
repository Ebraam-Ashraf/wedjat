/** StatusBadge — small colored pill for status (ok / warn / critical). */
export type BadgeStatus = 'ok' | 'warn' | 'critical' | 'idle';

export interface StatusBadgeProps {
  status: BadgeStatus;
  label?: string;
  dot?: boolean;
  className?: string;
}

const STATUS_COLORS: Record<BadgeStatus, string> = {
  ok:       'var(--ok)',
  warn:     'var(--warn)',
  critical: 'var(--critical)',
  idle:     'var(--text-dim)',
};

export default function StatusBadge({ status = 'idle', label, dot = true, className = '' }: StatusBadgeProps) {
  const color = STATUS_COLORS[status];
  return (
    <span
      className={`status-badge status-badge-${status} ${className}`.trim()}
      style={{
        display: 'inline-flex',
        alignItems: 'center',
        gap: 6,
        padding: '2px 8px',
        borderRadius: 12,
        fontSize: '0.78rem',
        fontWeight: 600,
        color,
        background: `color-mix(in srgb, ${color} 10%, transparent)`,
        border: `1px solid color-mix(in srgb, ${color} 33%, transparent)`,
      }}
    >
      {dot && (
        <i
          style={{ display: 'inline-block', width: 8, height: 8, borderRadius: '50%', background: color }}
        />
      )}
      {label ?? status}
    </span>
  );
}
