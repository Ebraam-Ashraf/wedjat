import { useEffect, useRef, useState, type ReactNode } from 'react';

const clamp = (value: number, min: number, max: number) => Math.min(max, Math.max(min, value));
const reducedMotion = () => typeof window !== 'undefined' && window.matchMedia?.('(prefers-reduced-motion: reduce)').matches;

export type Tone = 'ok' | 'warn' | 'crit' | 'dim' | 'accent';

export function toneFor(value: number | null | undefined, warn = 70, crit = 90): Tone {
  if (value == null || !Number.isFinite(value)) return 'dim';
  if (value >= crit) return 'crit';
  if (value >= warn) return 'warn';
  return 'accent';
}

const EIGHTHS = ['', '▏', '▎', '▍', '▌', '▋', '▊', '▉'];

export function meterString(value: number | null | undefined, width = 24) {
  if (value == null || !Number.isFinite(value)) return '·'.repeat(width);
  const cells = clamp(value, 0, 100) / 100 * width;
  let full = Math.floor(cells);
  const remainder = Math.round((cells - full) * 8);
  if (remainder === 8) full += 1;
  const partial = remainder && remainder < 8 ? EIGHTHS[remainder] : '';
  return '█'.repeat(full) + partial + '░'.repeat(Math.max(0, width - full - (partial ? 1 : 0)));
}

export function AsciiMeter({ pct, width = 24, tone, warn, crit, brackets = true }: {
  pct: number | null | undefined;
  width?: number;
  tone?: Tone;
  warn?: number;
  crit?: number;
  brackets?: boolean;
}) {
  const selectedTone = tone ?? toneFor(pct, warn, crit);
  return <span className="tk-meter" aria-hidden="true">
    {brackets && <span className="tk-dim">[</span>}
    <span className={`tk-${selectedTone} tk-meter-fill`}>{meterString(pct, width)}</span>
    {brackets && <span className="tk-dim">]</span>}
  </span>;
}

const SPARK = '▁▂▃▄▅▆▇█';
export function Sparkline({ values, width = 32, max = 100, tone = 'accent' }: { values: number[]; width?: number; max?: number; tone?: Tone }) {
  const data = values.slice(-width);
  const chars = data.map((value) => SPARK[clamp(Math.round(clamp(value, 0, max) / max * 7), 0, 7)]).join('');
  return <span className="tk-spark" aria-hidden="true">
    <span className="tk-dim">{'·'.repeat(Math.max(0, width - data.length))}</span>
    <span className={`tk-${tone}`}>{chars.slice(0, -1)}</span>
    <span className={`tk-${tone} tk-spark-head`}>{chars.slice(-1)}</span>
  </span>;
}

const NOISE = '0123456789#%&@$*';
export function ScrambleText({ text, duration = 320 }: { text: string; duration?: number }) {
  const [shown, setShown] = useState(text);
  const previous = useRef(text);
  useEffect(() => {
    if (previous.current === text || reducedMotion()) {
      previous.current = text;
      setShown(text);
      return;
    }
    previous.current = text;
    let frame = 0;
    const start = performance.now();
    const tick = (now: number) => {
      const progress = clamp((now - start) / duration, 0, 1);
      const settled = Math.floor(progress * text.length);
      setShown(text.split('').map((char, index) => index < settled || char === ' ' ? char : NOISE[Math.floor(Math.random() * NOISE.length)]).join(''));
      if (progress < 1) frame = requestAnimationFrame(tick);
    };
    frame = requestAnimationFrame(tick);
    return () => cancelAnimationFrame(frame);
  }, [text, duration]);
  return <span className="tk-scramble">{shown}</span>;
}

const SPINNERS = {
  braille: ['⠋', '⠙', '⠹', '⠸', '⠼', '⠴', '⠦', '⠧', '⠇', '⠏'],
  ankh: ['☥', '𓋹', '☥', '𓂀', '☥', '𓆣'],
  pulse: ['·', '•', '●', '•'],
  scan: ['▖', '▘', '▝', '▗'],
} as const;
export function Spinner({ kind = 'braille', interval = 90 }: { kind?: keyof typeof SPINNERS; interval?: number }) {
  const frames = SPINNERS[kind];
  const [index, setIndex] = useState(0);
  useEffect(() => {
    if (reducedMotion()) return;
    const timer = setInterval(() => setIndex((value) => (value + 1) % frames.length), interval);
    return () => clearInterval(timer);
  }, [frames.length, interval]);
  return <span className="tk-spinner" aria-hidden="true">{frames[index]}</span>;
}

const GLYPHS = '𓂀 𓋹 𓆣 𓇳 𓊽 𓁹 𓃭 𓅓 𓆓 𓈖 𓉐 𓊪 𓋴 𓌳 𓍯';
export function HieroMarquee({ text }: { text?: string }) {
  const line = `${GLYPHS} · ${text ?? 'WEDJAT · EYE OF HORUS WATCHES THE SILICON'} · `;
  return <div className="tk-marquee" aria-hidden="true"><div className="tk-marquee-track"><span>{line}</span><span>{line}</span></div></div>;
}

export function KV({ k, children, tone }: { k: string; children: ReactNode; tone?: Tone }) {
  return <div className="tk-kv"><span className="tk-k">{k.padEnd(6, ' ')}</span><span className={tone ? `tk-${tone}` : ''}>{children}</span></div>;
}

export function useRolling(value: number | null | undefined, key: unknown, size = 48) {
  const [history, setHistory] = useState<number[]>([]);
  useEffect(() => {
    if (value == null || !Number.isFinite(value)) return;
    setHistory((current) => [...current.slice(-(size - 1)), value]);
  }, [key, size, value]);
  return history;
}
