import { memo } from 'react';
import { Link } from 'react-router-dom';
import { throttleNames } from '../gpuData';
import { AsciiMeter, KV, ScrambleText, Spinner, toneFor } from './term';
import type { GpuInfo, GpuSample } from '../types';

const GB = 1_073_741_824;
const formatGB = (value: number | null | undefined) => value == null || !Number.isFinite(value) ? '--' : (value / GB).toFixed(1);
const watts = (value: number | null | undefined) => value == null || !Number.isFinite(value) ? null : value / 1000;
const percentOf = (value: number | null, total: number | null) => value == null || total == null || total <= 0 ? null : value / total * 100;

function GpuTermCard({ sample, meta, stale }: { sample: GpuSample; meta: GpuInfo | null; stale: boolean }) {
  const draw = watts(sample.power);
  const limit = watts(sample.power_limit);
  const drawPercent = percentOf(draw, limit);
  const totalVram = meta?.vram_total_bytes == null ? null : meta.vram_total_bytes;
  const vramPercent = sample.mem_util ?? percentOf(sample.mem, totalVram);
  const throttle = sample.throttle == null ? [] : throttleNames(sample.throttle);
  const utilTone = toneFor(sample.util, 85, 97);
  const tempTone = toneFor(sample.temp, 75, 85);
  const ecc = sample.ecc ?? 0;
  const state = stale
    ? { tone: 'crit' as const, label: 'STALE · LAST RECEIVED VALUES' }
    : throttle.length
      ? { tone: 'warn' as const, label: `THROTTLE · ${throttle.join(', ')}` }
      : ecc > 0
        ? { tone: 'warn' as const, label: `ECC ERRORS · ${ecc}` }
        : { tone: 'ok' as const, label: 'NOMINAL · THE EYE IS CALM' };

  return <article className={`gtc${stale ? ' is-stale' : ''}${throttle.length ? ' is-throttled' : ''}`} aria-label={`GPU ${sample.index}`}>
    <span className="gtc-scan" aria-hidden="true" />
    <header className="gtc-head">
      <span className="gtc-eye" aria-hidden="true">𓂀</span>
      <strong className="gtc-id">gpu{sample.index}</strong>
      <span className="gtc-name" title={meta?.name}>{meta?.name || 'unknown device'}</span>
      <span className="gtc-fill" aria-hidden="true" />
      <Link className="gtc-link" to={`/gpu/${encodeURIComponent(sample.uuid || String(sample.index))}`}>[ open ↵ ]</Link>
    </header>
    <div className="gtc-sub">
      <span className="tk-dim">uuid</span> <span className="gtc-uuid">{sample.uuid || '--'}</span>
      <span className="tk-dim"> · drv</span> {meta?.driver_version || '--'} <span className="tk-dim"> · </span>
    </div>
    <section className="gtc-hero">
      <div className="gtc-big">
        <span className={`gtc-big-num tk-${utilTone}`}><ScrambleText text={sample.util == null ? '--' : String(sample.util).padStart(3, ' ')} /></span>
        <span className="gtc-big-unit">%<br /><small>util</small></span>
      </div>
    </section>
    <div className="gtc-meter-row"><AsciiMeter pct={sample.util} width={48} tone={utilTone} /></div>
    <section className="gtc-grid">
      <KV k="TEMP"><AsciiMeter pct={sample.temp} width={16} tone={tempTone} /> <b className={`tk-${tempTone}`}>{sample.temp == null ? '--' : `${sample.temp}°C`}</b></KV>
      <KV k="PWR"><AsciiMeter pct={drawPercent} width={16} warn={80} crit={95} /> <b>{draw == null ? '--' : draw.toFixed(0)}</b><span className="tk-dim">/{limit == null ? '--' : limit.toFixed(0)} W</span></KV>
      <KV k="VRAM"><AsciiMeter pct={vramPercent} width={16} warn={80} crit={95} /> <b>{formatGB(sample.mem)}</b><span className="tk-dim">/{formatGB(totalVram)} G</span></KV>
      <KV k="CLK"><b>{sample.clock ?? '--'}</b><span className="tk-dim"> sm · </span><b>{sample.mem_clock ?? '--'}</b><span className="tk-dim"> mem MHz</span></KV>
      <KV k="ECC" tone={ecc > 0 ? 'warn' : 'dim'}>{sample.ecc ?? '--'}</KV>
      <KV k="THRTL" tone={throttle.length ? 'warn' : 'dim'}>{throttle.length ? throttle.join(' ') : 'none'}</KV>
    </section>
    <footer className={`gtc-status tk-bg-${state.tone}`}>
      <Spinner kind={state.tone === 'ok' ? 'ankh' : 'scan'} interval={state.tone === 'ok' ? 600 : 120} />
      <span>{state.label}</span><span className="gtc-fill" /><span>☥ wedjat://gpu{sample.index}</span>
    </footer>
  </article>;
}

export default memo(GpuTermCard);
