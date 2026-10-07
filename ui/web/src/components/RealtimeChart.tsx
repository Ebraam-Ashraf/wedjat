import { useEffect, useRef, memo } from 'react';
import ScopeRenderer from './scopeRenderer';
import { rafLoop } from '../store/rafLoop';
import { SYNTHETIC } from '../flags';
import type { TelemetryStoreLike } from '../types';

const WINDOW_MS = 60000;
const DELAY_MS = 500;
const HEIGHT = 240;

interface RealtimeChartProps {
  title: string;
  unit: string;
  store: TelemetryStoreLike;
  field: string;
  yDomain: [number, number];
  yTicks: number[];
  yFormat?: (v: number) => string;
  valueFormat?: (v: number) => string;
  windowMs?: number;
  delayMs?: number;
  height?: number;
  compareField?: string;
  compareColor?: string;
  gpuIndex?: number;
  scale?: number;
}

function RealtimeChart({
  title,
  unit,
  store,
  field,
  yDomain,
  yTicks,
  yFormat = (v: number) => String(Math.round(v)),
  valueFormat,
  windowMs = WINDOW_MS,
  delayMs = DELAY_MS,
  height = HEIGHT,
   compareField,
  compareColor,
  gpuIndex,
  scale = 1,
}: RealtimeChartProps) {
  const canvasRef = useRef<HTMLCanvasElement>(null);
  const timeSource = useRef(SYNTHETIC ? performance.now.bind(performance) : Date.now).current;
  const effectiveValueFormat = valueFormat || ((v: number) => `${Math.round(v / scale)}`);

  useEffect(() => {
    const canvas = canvasRef.current;
    if (!canvas) return undefined;

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

    const unsubscribe = rafLoop.add((_now: number) => renderer.draw(_now));
    return () => {
      unsubscribe();
      renderer.destroy();
    };
  }, [store, field, yDomain, yTicks, yFormat, effectiveValueFormat, windowMs, delayMs, timeSource, compareField, compareColor, gpuIndex, scale]);

  return (
    <div>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'baseline', marginBottom: 6 }}>
        <h4 style={{ margin: 0, textAlign: 'left', color: 'var(--text-dim)', fontWeight: 400 }}>
          {title}
        </h4>
        <span style={{ fontSize: '0.78rem', color: 'var(--text-dim)' }}>{unit}</span>
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

interface FilteredStore {
  gpuOrder: number[];
  gpu: (idx: number) => unknown;
  maxGapMs: () => number;
  lastMessageAt: number;
}

function createFilteredStore(store: TelemetryStoreLike, gpuIndex: number): FilteredStore {
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
    gpu: (idx: number) => (idx === gpuIndex ? record : null),
    maxGapMs: () => record.maxGapMs || 1500,
    lastMessageAt: store.lastMessageAt,
  };
}

export default memo(RealtimeChart);
