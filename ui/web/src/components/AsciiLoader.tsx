import { useEffect, useRef } from 'react';
import { renderPyramid } from './pyramidFrame';
import pyramidSolid from '../../../../assets/ascii/pyramid-solid.txt?raw';

/**
 * AsciiLoader — animated rotating pyramid for loading states.
 *
 * Renders via a <pre> ref (no React re-renders per frame).
 * Caps at 30 fps, pauses when tab is hidden (rAF does this),
 * and shows the static solid pyramid when prefers-reduced-motion is set.
 */

export interface AsciiLoaderProps {
  label?: string;
  speed?: number;
  w?: number;
  h?: number;
}

export default function AsciiLoader({ label = 'Loading', speed = 0.6, w = 56, h = 24 }: AsciiLoaderProps) {
  const ref = useRef<HTMLPreElement>(null);

  useEffect(() => {
    const el = ref.current;
    if (!el) return;

    // Reduced motion: show static pyramid
    if (window.matchMedia('(prefers-reduced-motion: reduce)').matches) {
      el.textContent = pyramidSolid || renderPyramid({ theta: 0.8, w, h });
      return;
    }

    let raf: number;
    let last = 0;
    const t0 = performance.now();

    const tick = (now: number) => {
      if (now - last >= 33) {
        last = now;
        el.textContent = renderPyramid({ theta: ((now - t0) / 1000) * speed, w, h });
      }
      raf = requestAnimationFrame(tick);
    };

    raf = requestAnimationFrame(tick);
    return () => cancelAnimationFrame(raf);
  }, [speed, w, h]);

  return (
    <figure className="ascii-loader" role="status" aria-live="polite">
      <pre ref={ref} className="ascii-pre" aria-hidden="true" />
      <figcaption className="ascii-label">{label}…</figcaption>
    </figure>
  );
}
