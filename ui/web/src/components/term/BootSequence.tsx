import { useEffect, useState } from 'react';
import pyramidArt from '../../../../../assets/ascii/pyramid-solid.txt?raw';

const LINES: Array<[string, string]> = [
  ['OK', 'loading wedjat kernel module ............. 𓂀'],
  ['OK', 'attaching eBPF probes to libcuda.so'],
  ['OK', 'opening NVML handle'],
  ['OK', 'mounting telemetry database'],
  ['OK', 'binding socket ws://…/socket'],
  ['..', 'summoning the eye of horus'],
  ['OK', 'all systems nominal · handing over to tty1'],
];
const KEY = 'wedjat-booted';

export default function BootSequence() {
  const [show, setShow] = useState(() => {
    try { return !window.matchMedia?.('(prefers-reduced-motion: reduce)').matches && sessionStorage.getItem(KEY) !== '1'; }
    catch { return false; }
  });
  const [line, setLine] = useState(0);
  const [leaving, setLeaving] = useState(false);

  useEffect(() => {
    if (!show) return;
    try { sessionStorage.setItem(KEY, '1'); } catch { /* unavailable */ }
    const timer = setInterval(() => setLine((value) => value + 1), 170);
    const skip = () => setLeaving(true);
    window.addEventListener('keydown', skip);
    window.addEventListener('pointerdown', skip);
    return () => { clearInterval(timer); window.removeEventListener('keydown', skip); window.removeEventListener('pointerdown', skip); };
  }, [show]);

  useEffect(() => { if (line >= LINES.length + 3) setLeaving(true); }, [line]);
  useEffect(() => {
    if (!leaving) return;
    const timer = setTimeout(() => setShow(false), 420);
    return () => clearTimeout(timer);
  }, [leaving]);

  if (!show) return null;
  return <div className={`tk-boot${leaving ? ' is-leaving' : ''}`} role="status" aria-label="Starting Wedjat">
    <pre className="tk-boot-art">{pyramidArt}</pre>
    <div className="tk-boot-log">
      <div className="tk-dim">WEDJAT GPU DAEMON · tty1 · press any key</div>
      {LINES.slice(0, line).map(([status, message], index) => <div key={index} className="tk-boot-line"><span className="tk-dim">[</span><span className={status === 'OK' ? 'tk-ok' : 'tk-warn'}>{status === 'OK' ? '  OK  ' : ' WAIT '}</span><span className="tk-dim">]</span> {message}</div>)}
      {line < LINES.length && <span className="term-cursor">█</span>}
    </div>
  </div>;
}
