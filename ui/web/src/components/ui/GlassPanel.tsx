import type { HTMLAttributes, ReactNode } from 'react';

/** GlassPanel — card with translucent background + border, themed via CSS variables. */
export interface GlassPanelProps extends HTMLAttributes<HTMLDivElement> {
  children: ReactNode;
}

export default function GlassPanel({ children, className = '', ...rest }: GlassPanelProps) {
  return (
    <div
      className={`glass-panel ${className}`}
      style={{
        padding: 24,
        borderRadius: 16,
      }}
      {...rest}
    >
      {children}
    </div>
  );
}
