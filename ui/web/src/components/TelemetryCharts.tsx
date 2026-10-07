import { useEffect, useRef, memo } from 'react';
import { LIVE_WINDOW_MS } from '../store/telemetryStore';
import { rafLoop } from '../store/rafLoop';
import { latestValue, pathFor, renderCursorTime } from './chartPaths';
import type { ChartSample } from './chartPaths';

const WIDTH = 320;
const HEIGHT = 64;

interface GraphSvgProps {
  samples: ChartSample[];
  field: string;
  className?: string;
  maxValue?: number;
  scale?: number;
  compareField?: string;
  windowMs?: number | null;
  maxGapMs?: number;
  intervalMs?: number;
  cadenceMeasured?: boolean;
  sampleSequence?: number;
}

const GraphSvg = memo(function GraphSvg({
  samples,
  field,
  className = '',
  maxValue,
  scale = 1,
  compareField,
  windowMs = LIVE_WINDOW_MS,
  maxGapMs,
  intervalMs = 500,
  cadenceMeasured = true,
  sampleSequence,
}: GraphSvgProps) {
  const mainPath = useRef<SVGPathElement>(null);
  const comparePath = useRef<SVGPathElement>(null);
  const modelRef = useRef<{
    samples: ChartSample[];
    field: string;
    className: string;
    maxValue?: number;
    scale: number;
    compareField?: string;
    windowMs: number | null;
    maxGapMs?: number;
    intervalMs: number;
    cadenceMeasured: boolean;
    historical: boolean;
  } | null>(null);

  const historical = windowMs == null;
  const model = {
    samples, field, className, maxValue, scale, compareField, windowMs,
    maxGapMs, intervalMs, cadenceMeasured, historical,
  };
  modelRef.current = model;

  useEffect(() => {
    if (historical) {
      drawPaths(modelRef.current, mainPath.current, comparePath.current);
      return undefined;
    }
    const unsubscribe = rafLoop.add(() => {
      drawPaths(modelRef.current, mainPath.current, comparePath.current);
    });
    return unsubscribe;
  }, [historical, windowMs]);

  useEffect(() => {
    drawPaths(modelRef.current, mainPath.current, comparePath.current);
  }, [samples, field, maxValue, scale, compareField, windowMs, maxGapMs, intervalMs, cadenceMeasured, sampleSequence, historical]);

  if (!samples.length)
    return <div className="chart-empty">{windowMs != null ? 'Collecting live samples…' : 'No stored samples'}</div>;

  const effectiveInterval = cadenceMeasured ? intervalMs : 500;
  const delay = Math.round(
    Number.isFinite(effectiveInterval) && effectiveInterval > 0 ? effectiveInterval : 500 * 1.2,
  );

  return (
    <svg
      className={`sparkline ${className}`}
      viewBox={`0 0 ${WIDTH} ${HEIGHT}`}
      preserveAspectRatio="none"
      role="img"
      aria-label={`${field} over ${historical ? 'stored history' : 'the last minute'}`}
      data-sequence={sampleSequence}
      data-visual-delay-ms={historical ? undefined : delay}
    >
      <path className="sparkline-grid" d={`M0 16H${WIDTH} M0 32H${WIDTH} M0 48H${WIDTH}`} />
      <path ref={mainPath} className="sparkline-line" d="" />
      {compareField && <path ref={comparePath} className="sparkline-compare" d="" />}
    </svg>
  );
});

function drawPaths(
  model: GraphSvgProps & { historical: boolean } | null,
  mainPath: SVGPathElement | null,
  comparePath: SVGPathElement | null,
  now: number = Date.now(),
): void {
  if (!model || !mainPath) return;

  const endTime = model.historical
    ? (model.samples.length ? Number(model.samples[model.samples.length - 1].t) : Date.now())
    : renderCursorTime(now, model.intervalMs, model.cadenceMeasured);

  const main = pathFor(
    model.samples, model.field, WIDTH, HEIGHT,
    model.maxValue || 0, model.scale ?? 1, endTime,
    model.windowMs ?? null, model.maxGapMs,
  ).join(' ');
  mainPath.setAttribute('d', main);

  if (comparePath && model.compareField) {
    const compare = pathFor(
      model.samples, model.compareField, WIDTH, HEIGHT,
      model.maxValue || 0, model.scale ?? 1, endTime,
      model.windowMs ?? null, model.maxGapMs,
    ).join(' ');
    comparePath.setAttribute('d', compare);
  }
}

export function Sparkline({
  samples = [],
  field = 'util',
  className = '',
  maxValue,
  scale = 1,
  compareField,
  windowMs = LIVE_WINDOW_MS,
  maxGapMs,
  intervalMs = 500,
  cadenceMeasured = true,
  sampleSequence,
}: Partial<GraphSvgProps>) {
  return (
    <GraphSvg
      samples={samples}
      field={field}
      className={className}
      maxValue={maxValue}
      scale={scale}
      compareField={compareField}
      windowMs={windowMs}
      maxGapMs={maxGapMs}
      intervalMs={intervalMs}
      cadenceMeasured={cadenceMeasured}
      sampleSequence={sampleSequence}
    />
  );
}

interface MetricChartProps {
  title: string;
  samples?: ChartSample[];
  field: string;
  unit: string;
  maxValue?: number;
  scale?: number;
  compareField?: string;
  compareLabel?: string;
  historical?: boolean;
  maxGapMs?: number;
  intervalMs?: number;
  cadenceMeasured?: boolean;
  sampleSequence?: number;
}

export function MetricChart({
  title,
  samples = [],
  field,
  unit,
  maxValue,
  scale = 1,
  compareField,
  compareLabel,
  historical = false,
  maxGapMs,
  intervalMs = 500,
  cadenceMeasured = true,
  sampleSequence,
}: MetricChartProps) {
  const latest = latestValue(samples, field);

  return (
    <section className="metric-chart panel">
      <div className="metric-chart-head">
        <h3>{title}</h3>
        <strong className="mono">
          {latest == null ? 'n/a' : `${Math.round(latest / scale).toLocaleString()}${unit}`}
        </strong>
      </div>
      <Sparkline
        samples={samples}
        field={field}
        maxValue={maxValue}
        scale={scale}
        compareField={compareField}
        windowMs={historical ? null : LIVE_WINDOW_MS}
        maxGapMs={maxGapMs ?? (historical ? 120_000 : undefined)}
        intervalMs={intervalMs}
        cadenceMeasured={cadenceMeasured}
        sampleSequence={sampleSequence}
      />
      {compareField && (
        <small className="compare-legend">
          <span><i className="legend-main" />Power draw</span>
          <span><i />{compareLabel || compareField}</span>
        </small>
      )}
      <small>
        {historical
          ? '1-minute buckets · stored history'
          : `Last 60 seconds · delayed visual cursor (~${((cadenceMeasured ? intervalMs : 500) * 1.2 / 1000).toFixed(1)} s) · latest value is an actual sample`}
      </small>
    </section>
  );
}
