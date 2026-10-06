import React, { useEffect, useRef } from 'react';
import ScopeRenderer from './scopeRenderer.js';
import { rafLoop } from '../store/rafLoop.js';
import { SYNTHETIC } from '../flags.js';

// A live scope.
//
// The canvas and its renderer are created once and reused for the lifetime of
// the component; the drawing itself happens inside the shared rAF loop and
// never passes through React state. A socket message therefore cannot cause a
// re-render here at all — the next frame simply reads the new sample.
//
// Props moved from `data`/`series` to `store`/`field`, because an imperative
// renderer reads the store directly instead of being handed a new array twice a
// second. The presentation props are unchanged.
//
// In synthetic mode, samples use performance.now() timestamps (monotonic).
// In live mode, samples use unix wall-clock ms (Date.now()).
// The renderer must use the matching time source.
const WINDOW_MS = 60000;
const DELAY_MS = 500;   // one sample interval: guarantees a real sample to the right
const HEIGHT = 240;

function RealtimeChart({
  title,
  unit,
  store,
  field,
  yDomain,
  yTicks,
  yFormat = (v) => Math.round(v),
  valueFormat,
  windowMs = WINDOW_MS,
  delayMs = DELAY_MS,
  height = HEIGHT,
  compareField,
  compareLabel,
  compareColor,
  gpuIndex,
  scale = 1,
}) {
  const canvasRef = useRef(null);
  // Synthetic mode uses performance.now(); live mode uses Date.now()
  const timeSource = useRef(SYNTHETIC ? performance.now.bind(performance) : Date.now).current;
  // Apply scale in valueFormat for legend display (raw values are in original units)
  const effectiveValueFormat = valueFormat || ((v) => `${Math.round(v / scale)}`);

  useEffect(() => {
    const canvas = canvasRef.current;
    if (!canvas) return undefined;

    // Create a filtered store view for a single GPU if gpuIndex is provided
    const filteredStore = gpuIndex != null ? createFilteredStore(store, gpuIndex) : store;

    const renderer = new ScopeRenderer(canvas, {
      store: filteredStore,
      field,
      windowMs,
      delayMs,
      yDomain,
      yTicks,
      yFormat,
      valueFormat: effectiveValueFormat,
      timeSource,
      compareField,
      compareColor,
      scale,
    });

    const unsubscribe = rafLoop.add((now) => renderer.draw(now));
    return () => {
      unsubscribe();
      renderer.destroy();
    };
    // The renderer reads yDomain/yTicks from opts each frame, so changing them
    // must rebuild it. Everything else is stable for the component's life.
  }, [store, field, yDomain, yTicks, yFormat, effectiveValueFormat, windowMs, delayMs, timeSource, compareField, compareColor, gpuIndex, scale]);

  return (
    <div>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'baseline', marginBottom: 6 }}>
        <h4 style={{ margin: 0, textAlign: 'left', color: 'var(--text-muted)', fontWeight: 400 }}>
          {title}
        </h4>
        <span style={{ fontSize: '0.78rem', color: 'var(--text-muted)' }}>{unit}</span>
      </div>

      <canvas
        ref={canvasRef}
        height={height}
        className="scope-canvas"
        role="img"
        aria-label={`${title} over the last ${windowMs / 1000} seconds`}
      />
    </div>
  );
}

// Create a store-like object that only contains data for a single GPU index
function createFilteredStore(store, gpuIndex) {
  const record = store.gpu(gpuIndex);
  if (!record) {
    return {
      gpuOrder: [],
      gpu: () => null,
      maxGapMs: () => 1500,
      lastMessageAt: store.lastMessageAt,
    };
  }
  return {
    gpuOrder: [gpuIndex],
    gpu: (idx) => idx === gpuIndex ? record : null,
    maxGapMs: () => record.maxGapMs || 1500,
    lastMessageAt: store.lastMessageAt,
  };
}

export default React.memo(RealtimeChart);
