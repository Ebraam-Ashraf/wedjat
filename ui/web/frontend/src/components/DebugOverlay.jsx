import React, { useEffect, useState } from 'react';
import { rafLoop } from '../store/rafLoop.js';

// Diagnostic overlay, shown with ?debug=1.
//
// Helps separate delayed GPU message arrivals from a slow browser render loop.
// Message intervals are measured when the browser handles each WebSocket event;
// they include daemon, socket, relay, and browser scheduling delays.
//
// It samples on its own slow interval rather than subscribing to the store, so
// it costs four renders a second and never distorts what it is measuring.

const SAMPLE_MS = 250;

function DebugOverlay({ store }) {
  const [snap, setSnap] = useState(null);

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

  const r1 = (v) => (v ? `${v.toFixed(0)}ms` : '--');
  const late = snap.avg > 700;
  const idle = rafLoop.fps === 0;
  const slowFrames = rafLoop.fps > 0 && rafLoop.fps < 30;

  const row = (label, value, colour) => (
    <div style={{ display: 'flex', justifyContent: 'space-between', gap: 18 }}>
      <span style={{ color: 'var(--text-muted)' }}>{label}</span>
      <span style={{ fontVariantNumeric: 'tabular-nums', color: colour || 'var(--text-main)' }}>{value}</span>
    </div>
  );

  return (
    <div style={{
      position: 'fixed', right: 16, bottom: 16, zIndex: 1000,
      background: 'rgba(15,23,42,0.94)',
      border: '1px solid var(--border-color)',
      borderRadius: 12, padding: '12px 14px',
      fontFamily: 'monospace', fontSize: 11, lineHeight: 1.7,
      minWidth: 260, pointerEvents: 'none',
    }}>
      <div style={{ color: 'var(--accent-primary)', fontWeight: 700, marginBottom: 6 }}>
        debug {snap.synthetic ? '(synthetic)' : ''}
      </div>
      {row('gpu interval avg', r1(snap.avg), late ? 'var(--accent-danger)' : 'var(--accent-success)')}
      {row('min / max', `${r1(snap.min)} / ${r1(snap.max)}`)}
      {row('last receive / source gap', `${r1(snap.lastReceiveGap)} / ${r1(snap.lastSampleGap)}`)}
      {row('GPU receive / sample age', `${r1(snap.gpuAge)} / ${r1(snap.sampleAge)}`)}
      {row('messages/sec', snap.rate.toFixed(1))}
      {row('total messages', snap.messages)}
      {row('last msg age', snap.age == null ? '--' : `${snap.age.toFixed(0)}ms`)}
      {row('rAF fps', snap.fps.toFixed(0), idle ? 'var(--text-muted)' : 'var(--accent-success)')}

      <div style={{ marginTop: 8, paddingTop: 6, borderTop: '1px solid var(--border-color)', color: 'var(--text-muted)' }}>
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
