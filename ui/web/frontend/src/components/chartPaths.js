import { monotoneBezierSegments } from './curve.js';

export function latestValue(samples, field) {
  const sample = samples[samples.length - 1];
  const value = sample?.[field];
  return value != null && Number.isFinite(Number(value)) ? value : null;
}

export function renderCursorTime(nowMs, intervalMs, cadenceMeasured = true) {
  const interval = cadenceMeasured && Number.isFinite(intervalMs) && intervalMs > 0 ? intervalMs : 500;
  return nowMs - interval * 1.2;
}

export function pathFor(samples, field, width, height, maxValue, scale, endTime, windowMs, maxGapMs) {
  const cutoff = windowMs == null ? -Infinity : endTime - windowMs;
  const series = samples
    .filter((sample) => windowMs == null || Number(sample.t) >= cutoff)
    .map((sample) => ({
      t: Number(sample.t),
      value: sample[field] == null ? null : Number(sample[field]) / scale,
    }));
  const valid = series.map((point) => point.value).filter(Number.isFinite);
  const max = maxValue || Math.max(1, ...valid);
  const firstTime = series.find((point) => Number.isFinite(point.t))?.t ?? endTime;
  const lastTime = series.length ? series[series.length - 1].t : endTime;
  const timeSpan = Math.max(1, lastTime - firstTime);
  const chunks = [];
  let chunk = [];
  let previousTime = null;

  const finishChunk = () => {
    if (chunk.length) {
      const segments = monotoneBezierSegments(chunk);
      if (!segments.length) {
        chunks.push(`M${chunk[0].x.toFixed(1)},${chunk[0].y.toFixed(1)}`);
      } else {
        chunks.push(segments.map((segment, index) => {
          const prefix = index === 0 ? `M${segment.start.x.toFixed(1)},${segment.start.y.toFixed(1)}` : '';
          return `${prefix} C${segment.control1.x.toFixed(1)},${segment.control1.y.toFixed(1)} ` +
            `${segment.control2.x.toFixed(1)},${segment.control2.y.toFixed(1)} ` +
            `${segment.end.x.toFixed(1)},${segment.end.y.toFixed(1)}`;
        }).join(''));
      }
    }
    chunk = [];
  };

  const xFor = (time) => windowMs == null
    ? (timeSpan <= 1 ? width : ((time - firstTime) / timeSpan) * width)
    : Math.max(0, Math.min(width, ((time - cutoff) / windowMs) * width));
  const verticalPadding = 2;
  const plotHeight = Math.max(1, height - verticalPadding * 2);
  const yFor = (value) => verticalPadding + plotHeight - (Math.max(0, value) / max) * plotHeight;

  series.forEach((point) => {
    // Live paths stop at the render cursor. A future real sample may only be
    // consulted below to interpolate that cursor; it is never plotted directly.
    if (windowMs != null && point.t > endTime) return;
    if (!Number.isFinite(point.value)) {
      finishChunk();
      previousTime = null;
      return;
    }
    if (previousTime != null && maxGapMs != null && point.t - previousTime > maxGapMs) finishChunk();
    chunk.push({ x: xFor(point.t), y: yFor(point.value) });
    previousTime = point.t;
  });

  if (windowMs != null) {
    let before = null;
    let after = null;
    for (const point of series) {
      if (point.t <= endTime) before = point;
      else { after = point; break; }
    }
    if (before && after && Number.isFinite(before.value) && Number.isFinite(after.value) &&
        (maxGapMs == null || after.t - before.t <= maxGapMs)) {
      const fraction = (endTime - before.t) / (after.t - before.t);
      const value = before.value + (after.value - before.value) * fraction;
      if (previousTime === before.t) {
        chunk.push({ x: xFor(endTime), y: yFor(value) });
      }
    }
  }

  finishChunk();
  return chunks.map((points) => points.join(' '));
}
