import { useEffect, useState, type ReactNode } from 'react';
import { Link, useLocation, useNavigate } from 'react-router-dom';
import { Spinner } from './term';
import BootSequence from './term/BootSequence';

/** Terminal window chrome: title bar, tmux-style window tabs, prompt line, bottom status line. */
export const TERMINAL_TABS = [
  { href: '/', name: 'live' },
  { href: '/processes', name: 'procs' },
  { href: '/incidents', name: 'incidents' },
  { href: '/history', name: 'history' },
] as const;

interface TerminalShellProps {
  children: ReactNode;
  header: ReactNode;
  connected: boolean;
  theme: string;
  onToggleTheme: () => void;
  gpuCount: number;
}

function useClock() {
  const [now, setNow] = useState(() => new Date());
  useEffect(() => {
    const t = setInterval(() => setNow(new Date()), 1000);
    return () => clearInterval(t);
  }, []);
  return now;
}

export default function TerminalShell({
  children,
  header,
  connected,
  theme,
  onToggleTheme,
  gpuCount,
}: TerminalShellProps) {
  const location = useLocation();
  const navigate = useNavigate();
  const now = useClock();
  const path = location.pathname;
  const activeIndex = TERMINAL_TABS.findIndex((t) =>
    t.href === '/' ? path === '/' : path.startsWith(t.href),
  );
  const cwd = path === '/' ? '~' : `~${path}`;
  const cmd =
    activeIndex >= 0 ? `wedjat ${TERMINAL_TABS[activeIndex].name}` : `cat ${path}`;

  // Keyboard: 1-5 switch windows, t toggles theme (ignored while typing)
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const el = e.target as HTMLElement | null;
      if (el && (el.tagName === 'INPUT' || el.tagName === 'SELECT' || el.tagName === 'TEXTAREA' || el.isContentEditable)) return;
      if (e.metaKey || e.ctrlKey || e.altKey) return;
      const n = Number(e.key);
      if (n >= 1 && n <= TERMINAL_TABS.length) {
        navigate(TERMINAL_TABS[n - 1].href);
      } else if (e.key === 't') {
        onToggleTheme();
      }
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [navigate, onToggleTheme]);

  const time = now.toLocaleTimeString([], { hour12: false });
  const date = now.toISOString().slice(0, 10);

  return (
    <div className="term-window">
      <BootSequence />
      {/* Title bar */}
      <div className="term-titlebar">
        <span className="term-dots" aria-hidden="true">
          <i /> <i /> <i />
        </span>
        <span className="term-title"><span className="tk-glyph-pulse">𓂀</span> wedjat@gpu-host: {cwd} — {cmd}</span>
      </div>

      {header}

      {/* tmux-style window list */}
      <nav className="term-tabs" aria-label="Main navigation">
        <span className="term-session">[wedjat]</span>
        {TERMINAL_TABS.map((t, i) => (
          <Link
            key={t.href}
            to={t.href}
            className={`term-tab${i === activeIndex ? ' is-active' : ''}`}
            aria-current={i === activeIndex ? 'page' : undefined}
          >
            {i + 1}:{t.name}
            {i === activeIndex ? '*' : ' '}
          </Link>
        ))}
      </nav>

      {/* Prompt line */}
      <div className="term-prompt" aria-hidden="true">
        <span className="term-user">wedjat@gpu-host</span>
        <span className="term-sep">:</span>
        <span className="term-cwd">{cwd}</span>
        <span className="term-sep">$ </span>
        <span className="term-cmd">{cmd}</span>
        <span className="term-cursor">█</span>
      </div>

      <main key={path} className="term-body page-wrap term-enter">{children}</main>

      {/* Bottom status line */}
      <footer className="term-statusline">
        <span className="term-mode">{connected ? ' NORMAL ' : ' OFFLINE '}</span>
        <span className="term-status-item"><Spinner kind={connected ? 'braille' : 'pulse'} interval={connected ? 90 : 400} /> {connected ? 'streaming' : 'waiting'}</span>
        <span className="term-status-item">gpus:{gpuCount}</span>
        <span className="term-status-item">sock:{connected ? 'up' : 'down'}</span>
        <span className="term-status-hint">[1-5] switch · [t] theme</span>
        <span className="term-status-spacer" />
        <span className="term-status-item">{date}</span>
        <span className="term-status-clock">{time}</span>
      </footer>
    </div>
  );
}
