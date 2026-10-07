import { monotoneBezierSegments } from './curve';
import type { BezierSegment, Point2D } from './curve';

export interface ChartSample {
  t: number;
  [key: string]: number | undefined;
}

interface SeriesPoint {
  t: number;
  value: number | null;
}

export function latestValue(samples: ChartSample[], field: string): number | null {
  const sample = samples[samples.length - 1];
  const value = sample?.[field];
  return value != null && Number.isFinite(Number(value)) ? Number(value) : null;
}

export function renderCursorTime(nowMs: number, intervalMs: number | undefined, cadenceMeasured = true): number {
  const interval = cadenceMeasured && intervalMs != null && intervalMs > 0 ? intervalMs : 500;
  return nowMs - interval * 1.2;
}

export function pathFor(
  samples: ChartSample[],
  field: string,
  width: number,
  height: number,
  maxValue: number,
  scale: number,
  endTime: number,
  windowMs: number | null,
  maxGapMs: number | undefined,
): string[] {
  const cutoff = windowMs == null ? -Infinity : endTime - windowMs;
  const series: SeriesPoint[] = samples
    .filter((sample) => windowMs == null || Number(sample.t) >= cutoff)
    .map((sample) => ({
      t: Number(sample.t),
      value: sample[field] == null ? null : Number(sample[field]) / scale,
    }));
  const valid = series.map((p) => p.value).filter((v): v is number => v != null && Number.isFinite(v));
  const max = maxValue || Math.max(1, ...valid);
  const firstTime = series.find((p) => Number.isFinite(p.t))?.t ?? endTime;
  const lastTime = series.length ? series[series.length - 1].t : endTime;
  const timeSpan = Math.max(1, lastTime - firstTime);
  const chunks: string[] = [];
  let chunk: Point2D[] = [];
  let previousTime: number | null = null;

  const finishChunk = () => {
    if (chunk.length) {
      const segments: BezierSegment[] = monotoneBezierSegments(chunk);
      if (!segments.length) {
        chunks.push(`M${chunk[0].x.toFixed(1)},${chunk[0].y.toFixed(1)}`);
      } else {
        chunks.push(
          segments
            .map((segment, index) => {
              const prefix = index === 0
                ? `M${segment.start.x.toFixed(1)},${segment.start.y.toFixed(1)}`
                : '';
              return (
                `${prefix} C${segment.control1.x.toFixed(1)},${segment.control1.y.toFixed(1)} ` +
                `${segment.control2.x.toFixed(1)},${segment.control2.y.toFixed(1)} ` +
                `${segment.end.x.toFixed(1)},${segment.end.y.toFixed(1)}`
              );
            })
            .join(''),
        );
      }
    }
    chunk = [];
  };

  const xFor = (time: number): number =>
    windowMs == null
      ? (timeSpan <= 1 ? width : ((time - firstTime) / timeSpan) * width)
      : Math.max(0, Math.min(width, ((time - cutoff) / windowMs) * width));
  const verticalPadding = 2;
  const plotHeight = Math.max(1, height - verticalPadding * 2);
  const yFor = (value: number): number =>
    verticalPadding + plotHeight - (Math.max(0, value) / max) * plotHeight;

  series.forEach((point) => {
    if (windowMs != null && point.t > endTime) return;
    if (!Number.isFinite(point.value as number)) {
      finishChunk();
      previousTime = null;
      return;
    }
    if (previousTime != null && maxGapMs != null && point.t - previousTime > maxGapMs) finishChunk();
    chunk.push({ x: xFor(point.t), y: yFor(point.value as number) });
    previousTime = point.t;
  });

  if (windowMs != null) {
    let before: SeriesPoint | null = null;
    let after: SeriesPoint | null = null;
    for (const point of series) {
      if (point.t <= endTime) before = point;
      else { after = point; break; }
    }
    if (
      before && after &&
      Number.isFinite(before.value as number) && Number.isFinite(after.value as number) &&
      (maxGapMs == null || after.t - before.t <= maxGapMs)
    ) {
      const fraction = (endTime - before.t) / (after.t - before.t);
      const value = before.value! + (after.value! - before.value!) * fraction;
      if (previousTime === before.t) {
        chunk.push({ x: xFor(endTime), y: yFor(value) });
      }
    }
  }

  finishChunk();
  return chunks;
}
