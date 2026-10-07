import { useEffect, useState } from 'react';
import { rafLoop } from '../store/rafLoop';
import type { TelemetryStoreLike } from '../types';

const SAMPLE_MS = 250;

interface SnapState {
  avg: number;
  min: number;
  max: number;
  rate: number;
  fps: number;
  messages: number;
  age: number | null;
  gpuAge: number | null;
  sampleAge: number | null;
  lastReceiveGap: number;
  lastSampleGap: number;
  synthetic: boolean;
}

interface DebugOverlayProps {
  store: TelemetryStoreLike;
}

const r1 = (v: number | null): string => (v ? `${v.toFixed(0)}ms` : '--');

function DebugOverlay({ store }: DebugOverlayProps) {
  const [snap, setSnap] = useState<SnapState | null>(null);

  useEffect(() => {
    const read = () => {
      const s = store.stats;
      const age = store.lastMessageAt ? performance.now() - store.lastMessageAt : null;
      const gpuAge = store.lastGpuAt ? performance.now() - store.lastGpuAt : null;
      const sampleAge = store.lastGpuSampleTimestampMs
        ? Date.now() - store.lastGpuSampleTimestampMs
        : null;
      setSnap({
        avg: s.avgInterval,
        min: s.minInterval,
        max: s.maxInterval,
        rate: s.rate,
        fps: rafLoop.fps,
        messages: s.messages,
        age,
        gpuAge,
        sampleAge,
        lastReceiveGap: s.lastReceiveGapMs,
        lastSampleGap: s.lastSampleGapMs,
        synthetic: store.synthetic,
      });
    };
    read();
    const id = setInterval(read, SAMPLE_MS);
    return () => clearInterval(id);
  }, [store]);

  if (!snap) return null;

  const late = snap.avg > 700;
  const idle = rafLoop.fps === 0;
  const slowFrames = rafLoop.fps > 0 && rafLoop.fps < 30;

  const row = (label: string, value: string, colour?: string) => (
    <div style={{ display: 'flex', justifyContent: 'space-between', gap: 18 }}>
      <span style={{ color: 'var(--text-dim)' }}>{label}</span>
      <span style={{ fontVariantNumeric: 'tabular-nums', color: colour || 'var(--text)' }}>{value}</span>
    </div>
  );

  return (
    <div
      style={{
        position: 'fixed', right: 16, bottom: 16, zIndex: 1000,
        background: 'var(--panel)',
        border: '1px solid var(--border)',
        borderRadius: 12, padding: '12px 14px',
        fontFamily: 'monospace', fontSize: 11, lineHeight: 1.7,
        minWidth: 260, pointerEvents: 'none',
      }}
    >
      <div style={{ color: 'var(--chart-line)', fontWeight: 700, marginBottom: 6 }}>
        debug {snap.synthetic ? '(synthetic)' : ''}
      </div>
      {row('gpu interval avg', r1(snap.avg), late ? 'var(--critical)' : 'var(--ok)')}
      {row('min / max', `${r1(snap.min)} / ${r1(snap.max)}`)}
      {row('last receive / source gap', `${r1(snap.lastReceiveGap)} / ${r1(snap.lastSampleGap)}`)}
      {row('GPU receive / sample age', `${r1(snap.gpuAge)} / ${r1(snap.sampleAge)}`)}
      {row('messages/sec', snap.rate.toFixed(1))}
      {row('total messages', String(snap.messages))}
      {row('last msg age', snap.age == null ? '--' : `${snap.age.toFixed(0)}ms`)}
      {row('rAF fps', snap.fps.toFixed(0), idle ? 'var(--text-dim)' : 'var(--ok)')}

      <div
        style={{
          marginTop: 8, paddingTop: 6,
          borderTop: '1px solid var(--border)',
          color: 'var(--text-dim)',
        }}
      >
        {idle
          ? 'loop parked (tab hidden)'
          : slowFrames
            ? 'low browser FPS: UI may be busy or throttled'
            : late
              ? 'GPU messages arrive late; see console gap details'
              : 'GPU message cadence looks healthy'}
      </div>
    </div>
  );
}

export default DebugOverlay;
