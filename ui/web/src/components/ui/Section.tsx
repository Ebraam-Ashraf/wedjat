import type { HTMLAttributes, ReactNode } from 'react';

/** Section — a titled, rounded panel used for grouping related controls/metrics. */
export interface SectionProps extends HTMLAttributes<HTMLElement> {
  title: string;
  icon?: string;
  hint?: ReactNode;
}

export default function Section({ title, icon, hint, className = '', children, ...rest }: SectionProps) {
  return (
    <section className={`section ${className}`} {...rest}>
      <header className="section-header">
        <h2 className="section-title">
          {icon && <span className="section-icon">{icon}</span>}
          {title}
        </h2>
        {hint && <div className="section-hint">{hint}</div>}
      </header>
      <div className="section-body">{children}</div>
    </section>
  );
}
