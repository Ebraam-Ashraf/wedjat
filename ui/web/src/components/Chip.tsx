import type { CSSProperties, ReactNode } from 'react';

/**
 * Chip — inline monospace tag for GPU names, PIDs, status tags and units.
 * Variants: default (ghost), status (ok/warn/critical), active.
 */

export type ChipVariant = 'default' | 'ok' | 'warn' | 'critical' | 'active' | 'neutral';

export interface ChipProps {
  children: ReactNode;
  variant?: ChipVariant;
  className?: string;
  style?: CSSProperties;
}

const VARIANT_MAP: Record<string, string> = {
  default:  '',
  ok:       'chip-ok',
  warn:     'chip-warn',
  critical: 'chip-critical',
  active:   'chip-active',
  neutral:  '',
};

export default function Chip({ children, variant = 'default', className = '', style }: ChipProps) {
  return (
    <span className={`chip ${VARIANT_MAP[variant] ?? ''} ${className}`.trim()} style={style}>
      {children}
    </span>
  );
}
