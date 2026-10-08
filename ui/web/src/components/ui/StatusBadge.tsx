/** StatusBadge — bracketed terminal tag for status (ok / warn / critical). */
export type BadgeStatus = 'ok' | 'warn' | 'critical' | 'idle';

export interface StatusBadgeProps {
  status: BadgeStatus;
  label?: string;
  dot?: boolean;
  className?: string;
}

const STATUS_COLORS: Record<BadgeStatus, string> = {
  ok: 'var(--ok)',
  warn: 'var(--warn)',
  critical: 'var(--critical)',
  idle: 'var(--text-dim)',
};

export default function StatusBadge({ status = 'idle', label, dot = true, className = '' }: StatusBadgeProps) {
  const color = STATUS_COLORS[status];
  return (
    <span
      className={`status-badge status-badge-${status} ${className}`.trim()}
      style={{ color, fontWeight: 700, fontSize: '0.78rem', textTransform: 'uppercase' }}
    >
      {dot && <span aria-hidden="true">● </span>}
      {label ?? status}
    </span>
  );
}
