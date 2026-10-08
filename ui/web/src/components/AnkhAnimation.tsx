import { useEffect, useRef, useState } from 'react';
import ankhRaw from '../../../../assets/ascii/ankh.txt?raw';

// ---------- load art -> boolean grid ----------
interface Art {
  grid: boolean[][];
  w: number;
  h: number;
}

function loadArt(raw: string): Art {
  let lines = raw.replace(/\s+$/, '').split('\n');
  const indent = Math.min(
    ...lines.filter((l) => l.trim()).map((l) => (l.match(/^ */) as RegExpMatchArray)[0].length),
  );
  lines = lines.map((l) => l.slice(indent));
  const w = Math.max(...lines.map((l) => l.trimEnd().length));
  return {
    grid: lines.map((l) =>
      Array.from({ length: w }, (_, x) => (l[x] || ' ') !== ' '),
    ),
    w,
    h: lines.length,
  };
}

const RAMP = ' .:-=+*#%@';
const SS = 2; // supersampling per axis

interface Frame {
  rows: string[];
  outW: number;
  outH: number;
  brightness: number;
}

function renderFrame(art: Art, theta: number, availW: number, availH: number): Frame {
  const s = Math.min(availW / art.w, availH / art.h, 1); // never upscale
  const outW = Math.max(1, Math.round(art.w * s));
  const outH = Math.max(1, Math.round(art.h * s));
  const cos = Math.cos(theta);
  const w = Math.max(Math.abs(cos), 0.03); // never fully vanish edge-on
  const flip = cos < 0;                    // back side = mirrored
  const rows: string[] = [];

  for (let oy = 0; oy < outH; oy++) {
    let row = '';
    for (let ox = 0; ox < outW; ox++) {
      let hit = 0;
      for (let j = 0; j < SS; j++) {
        for (let i = 0; i < SS; i++) {
          const dx = (ox + (i + 0.5) / SS - outW / 2) / (s * w);
          let sx = art.w / 2 + dx;
          if (flip) sx = art.w - sx;
          const sy = (oy + (j + 0.5) / SS) / s;
          const gx = Math.floor(sx);
          const gy = Math.floor(sy);
          if (gx >= 0 && gx < art.w && gy >= 0 && gy < art.h && art.grid[gy][gx]) hit++;
        }
      }
      row += RAMP[Math.round((hit / (SS * SS)) * (RAMP.length - 1))];
    }
    rows.push(row);
  }

  return { rows, outW, outH, brightness: 0.4 + 0.6 * Math.abs(cos) };
}

// Gold color dimmed by brightness
function goldColor(b: number): string {
  const r = Math.round(212 * b);
  const g = Math.round(175 * b);
  const bl = Math.round(55 * b);
  return `rgb(${r},${g},${bl})`;
}

// Pre-parse art once at module level
const ART = loadArt(ankhRaw);

interface AnkhAnimationProps {
  /** Rotation speed in radians per second (default 1.2) */
  speed?: number;
  /** Available width in characters (default 56) */
  w?: number;
  /** Available height in lines (default 40) */
  h?: number;
  /** Enable gold truecolor (default true) */
  color?: boolean;
  className?: string;
}

export default function AnkhAnimation({
  speed = 1.2,
  w = 56,
  h = 40,
  color = true,
  className = '',
}: AnkhAnimationProps) {
  const [html, setHtml] = useState('');
  const animRef = useRef<number | null>(null);
  const t0Ref = useRef<number | null>(null);
  const lastRef = useRef(0);

  useEffect(() => {
    t0Ref.current = null;

    const animate = (now: number) => {
      if (t0Ref.current === null) t0Ref.current = now;

      if (now - lastRef.current >= 1000 / 30) {
        lastRef.current = now;
        const theta = ((now - t0Ref.current) / 1000) * speed;
        const f = renderFrame(ART, theta, w, h);

        if (color) {
          const col = goldColor(f.brightness);
          setHtml(
            f.rows
              .map((row) => `<span style="color:${col}">${row}</span>`)
              .join('\n'),
          );
        } else {
          setHtml(f.rows.join('\n'));
        }
      }

      animRef.current = requestAnimationFrame(animate);
    };

    animRef.current = requestAnimationFrame(animate);
    return () => {
      if (animRef.current !== null) cancelAnimationFrame(animRef.current);
    };
  }, [speed, w, h, color]);

  return (
    <pre
      className={`font-mono leading-none whitespace-pre select-none ${className}`}
      style={{
        fontFamily: 'var(--font-mono), ui-monospace, SFMono-Regular, Menlo, Consolas, monospace',
        fontSize: 'var(--text-xs, 9px)',
        lineHeight: '1.15',
        whiteSpace: 'pre',
        userSelect: 'none',
        fontVariantLigatures: 'none',
      }}
      dangerouslySetInnerHTML={{ __html: html }}
      aria-hidden="true"
    />
  );
}
