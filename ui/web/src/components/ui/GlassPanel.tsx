import type { HTMLAttributes, ReactNode } from 'react';

/** GlassPanel — now a flat terminal box, themed via CSS variables. */
export interface GlassPanelProps extends HTMLAttributes<HTMLDivElement> {
  children: ReactNode;
}

export default function GlassPanel({ children, className = '', ...rest }: GlassPanelProps) {
  return (
    <div className={`glass-panel ${className}`} {...rest}>
      {children}
    </div>
  );
}
