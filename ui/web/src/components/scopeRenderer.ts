import { monotoneBezierSegments } from './curve';
import type { TelemetryRecord, GpuSample } from '../types';

const GRID_LINE = 'rgba(148,163,184,0.58)';
const AXIS_LINE = 'rgba(203,213,225,0.80)';
const LABEL = 'rgba(226,232,240,0.88)';
const BG = '#0f172a';

const GUTTER_L = 42;
const GUTTER_B = 22;
const LEGEND_H = 22;
const MAX_BACKING_PX = 4096;

const SERIES_COLORS = ['#3b82f6', '#8b5cf6', '#10b981', '#f59e0b', '#ec4899', '#06b6d4'];
export const seriesColor = (i: number): string => SERIES_COLORS[i % SERIES_COLORS.length];

function getField(sample: GpuSample, field: string): number | null | undefined {
  return (sample as unknown as Record<string, unknown>)[field] as number | null | undefined;
}

interface ScopeOptions {
  store: object;
  field: string;
  windowMs: number;
  delayMs: number;
  yDomain: [number, number];
  yTicks: number[];
  valueFormat: (v: number) => string;
  yFormat?: (v: number) => string;
  timeSource?: () => number;
  compareField?: string;
  compareColor?: string;
  scale?: number;
}

interface StoreLike {
  gpuOrder: number[];
  gpu(index: number): TelemetryRecord | undefined;
  maxGapMs(): number;
  lastMessageAt: number;
}

interface PlotRect {
  x: number;
  y: number;
  w: number;
  h: number;
}

interface SamplePoint {
  x: number;
  y: number;
}

export class ScopeRenderer {
  canvas: HTMLCanvasElement;
  ctx: CanvasRenderingContext2D;
  opts: ScopeOptions;
  cssW = 0;
  cssH = 0;
  dpr = 0;
  scale = 1;
  private _timeSource: () => number;
  private _ro: ResizeObserver;
  private _onResize: () => void;

  constructor(canvas: HTMLCanvasElement, opts: ScopeOptions) {
    this.canvas = canvas;
    this.ctx = canvas.getContext('2d')!;
    this.opts = opts;
    this._timeSource = opts.timeSource || (() => Date.now());
    this._onResize = () => this.resize();
    this._ro = new ResizeObserver(this._onResize);
    this._ro.observe(canvas.parentElement || canvas);
    this.resize();
  }

  destroy(): void {
    this._ro.disconnect();
  }

  resize(): void {
    const dpr = window.devicePixelRatio || 1;
    const rect = this.canvas.getBoundingClientRect();
    const w = Math.max(1, Math.round(rect.width));
    const h = Math.max(1, Math.round(rect.height));
    if (w === this.cssW && h === this.cssH && dpr === this.dpr) return;

    this.cssW = w;
    this.cssH = h;
    this.dpr = dpr;
    const scale = Math.max(0.01, Math.min(dpr, MAX_BACKING_PX / w, MAX_BACKING_PX / h));
    this.scale = scale;
    this.canvas.width = Math.round(w * scale);
    this.canvas.height = Math.round(h * scale);
    this.ctx.setTransform(scale, 0, 0, scale, 0, 0);
  }

  get plot(): PlotRect {
    return {
      x: GUTTER_L,
      y: LEGEND_H,
      w: Math.max(1, this.cssW - GUTTER_L),
      h: Math.max(1, this.cssH - LEGEND_H - GUTTER_B),
    };
  }

  draw(_now: number): void {
    const { ctx, opts } = this;
    const p = this.plot;
    ctx.clearRect(0, 0, this.cssW, this.cssH);

    const store: StoreLike = opts.store as StoreLike;
    const windowMs = opts.windowMs;
    const wallNow = this._timeSource();
    const renderT = wallNow - opts.delayMs;
    const winStart = renderT - windowMs;

    this._drawGrid(p, opts, renderT, windowMs);

    const order = store.gpuOrder;
    const hasData = order.length > 0;
    this._drawLegend(p, store, order);

    if (!hasData) {
      this._drawWatermark(p, store.lastMessageAt ? 'Waiting for GPU telemetry…' : 'No data yet');
      return;
    }

    const maxGap = store.maxGapMs();
    for (let i = 0; i < order.length; i++) {
      const rec = store.gpu(order[i]);
      if (rec) {
        this._drawSeries(p, rec, opts.field, renderT, winStart, maxGap, seriesColor(i));
        if (opts.compareField) {
          const compareColor = opts.compareColor || '#06b6d4';
          this._drawSeries(p, rec, opts.compareField, renderT, winStart, maxGap, compareColor);
        }
      }
    }
  }

  private _drawGrid(p: PlotRect, opts: ScopeOptions, _renderT: number, windowMs: number): void {
    const { ctx } = this;
    const [lo, hi] = opts.yDomain;

    ctx.save();
    ctx.font = '11px Outfit, sans-serif';

    ctx.textAlign = 'right';
    ctx.textBaseline = 'middle';
    for (const v of opts.yTicks) {
      const y = Math.round(p.y + p.h - ((v - lo) / (hi - lo)) * p.h) + 0.5;
      ctx.strokeStyle = GRID_LINE;
      ctx.lineWidth = 1;
      ctx.beginPath();
      ctx.moveTo(p.x, y);
      ctx.lineTo(p.x + p.w, y);
      ctx.stroke();
      ctx.fillStyle = LABEL;
      ctx.fillText(opts.yFormat ? opts.yFormat(v) : String(v), p.x - 8, y);
    }

    const divisions = 6;
    ctx.textAlign = 'center';
    ctx.textBaseline = 'top';
    for (let i = 0; i <= divisions; i++) {
      const secondsAgo = Math.round((windowMs - (windowMs / divisions) * i) / 1000);
      const x = Math.round(p.x + (i / divisions) * p.w) + 0.5;
      ctx.strokeStyle = i === divisions ? AXIS_LINE : GRID_LINE;
      ctx.lineWidth = i === divisions ? 1.5 : 1;
      ctx.beginPath();
      ctx.moveTo(x, p.y);
      ctx.lineTo(x, p.y + p.h);
      ctx.stroke();
      ctx.fillStyle = LABEL;
      ctx.textAlign = i === divisions ? 'right' : i === 0 ? 'left' : 'center';
      const label = secondsAgo === 0 ? 'now' : secondsAgo === 60 ? '1 min' : `${secondsAgo} sec`;
      ctx.fillText(label, x, p.y + p.h + 6);
    }

    ctx.restore();
  }

  private _drawLegend(p: PlotRect, store: StoreLike, order: number[]): void {
    const { ctx } = this;
    ctx.save();
    ctx.font = '12px Outfit, sans-serif';
    ctx.textBaseline = 'middle';
    ctx.textAlign = 'left';

    let x = p.x;
    for (let i = 0; i < order.length; i++) {
      const rec = store.gpu(order[i]);
      if (!rec) continue;
      const color = seriesColor(i);
      const latest = this._latestValue(rec, this.opts.field);
      const name = `GPU ${rec.index}`;
      const val = latest == null ? '--' : this.opts.valueFormat(latest);

      ctx.fillStyle = color;
      ctx.fillRect(x, 9, 10, 2);

      ctx.fillStyle = LABEL;
      ctx.fillText(name, x + 15, 10);
      const nameW = ctx.measureText(name).width;

      ctx.fillStyle = color;
      ctx.fillText(val, x + 15 + nameW + 6, 10);
      const valW = ctx.measureText(val).width;

      x += 15 + nameW + 6 + valW + 18;
    }
    ctx.restore();
  }

  private _drawWatermark(p: PlotRect, text: string): void {
    const { ctx } = this;
    ctx.save();
    ctx.font = '13px Outfit, sans-serif';
    ctx.fillStyle = LABEL;
    ctx.textAlign = 'center';
    ctx.textBaseline = 'middle';
    ctx.fillText(text, p.x + p.w / 2, p.y + p.h / 2);
    ctx.restore();
  }

  private _latestValue(rec: TelemetryRecord, field: string): number | null {
    const ring = rec.ring;
    for (let i = ring.length - 1; i >= 0; i--) {
      const v = getField(ring[i], field);
      if (v != null && Number.isFinite(v)) return v;
    }
    return null;
  }

  private _drawSeries(
    p: PlotRect,
    rec: TelemetryRecord,
    field: string,
    renderT: number,
    winStart: number,
    maxGap: number,
    color: string,
  ): void {
    const { ctx } = this;
    const [lo, hi] = this.opts.yDomain;
    const span = hi - lo;
    const scale = this.opts.scale || 1;
    const pts: (SamplePoint | null)[] = [];

    const toX = (t: number): number => p.x + ((t - winStart) / (renderT - winStart)) * p.w;

    const VERTICAL_PADDING = 2;
    const plotHeight = Math.max(1, p.h - VERTICAL_PADDING * 2);
    const toY = (v: number): number => {
      const scaled = v / scale;
      const c = Math.min(hi, Math.max(lo, scaled));
      return p.y + VERTICAL_PADDING + plotHeight - ((c - lo) / span) * plotHeight;
    };

    const ring = rec.ring;
    const n = ring.length;
    if (n === 0) return;

    let lo2 = 0, hi2 = n;
    while (lo2 < hi2) {
      const mid = (lo2 + hi2) >> 1;
      if (ring[mid].t! < winStart) lo2 = mid + 1;
      else hi2 = mid;
    }
    const startIdx = lo2;

    let prev: GpuSample | null = startIdx > 0 ? ring[startIdx - 1] : null;
    let next: GpuSample | null = null;
    for (let i = startIdx; i < n; i++) {
      const s = ring[i];
      if (s.t! <= renderT) prev = s;
      else { next = s; break; }
    }

    if (!prev) return;

    let headVal: number | null | undefined = getField(prev, field);
    if (next && next.t! - prev.t! <= maxGap) {
      const va = getField(prev, field);
      const vb = getField(next, field);
      if (va != null && vb != null) {
        const f = Math.max(0, Math.min(1, (renderT - prev.t!) / (next.t! - prev.t!)));
        headVal = va + (vb - va) * f;
      } else if (va == null) {
        headVal = vb;
      }
    }

    let prevDrawn: GpuSample | null = null;
    if (startIdx > 0) {
      const outside = ring[startIdx - 1];
      const inside = ring[startIdx];
      if (
        inside &&
        getField(outside, field) != null && getField(inside, field) != null &&
        inside.t! - outside.t! <= maxGap
      ) {
        const f = (winStart - outside.t!) / (inside.t! - outside.t!);
        const ov = getField(outside, field) as number;
        const iv = getField(inside, field) as number;
        const vEdge = ov + (iv - ov) * f;
        pts.push({ x: p.x, y: toY(vEdge) });
        prevDrawn = outside;
      }
    }

    for (let i = startIdx; i < n; i++) {
      const s = ring[i];
      if (s.t! > renderT) break;
      const v = getField(s, field);
      if (v == null) continue;
      if (prevDrawn && s.t! - prevDrawn.t! > maxGap) pts.push(null);
      pts.push({ x: toX(s.t!), y: toY(v) });
      prevDrawn = s;
    }

    if (headVal != null && Number.isFinite(headVal)) {
      if (prevDrawn && renderT - prevDrawn.t! > maxGap) pts.push(null);
      pts.push({ x: p.x + p.w, y: toY(headVal) });
    }

    const drawable = pts.filter(Boolean) as SamplePoint[];
    if (drawable.length === 0) return;

    ctx.save();
    ctx.strokeStyle = color;
    ctx.lineWidth = 2;
    ctx.lineJoin = 'round';
    ctx.lineCap = 'round';
    ctx.beginPath();

    let chunk: SamplePoint[] = [];
    const drawChunk = () => {
      if (!chunk.length) return;
      ctx.moveTo(chunk[0].x, chunk[0].y);
      for (const segment of monotoneBezierSegments(chunk)) {
        ctx.bezierCurveTo(
          segment.control1.x, segment.control1.y,
          segment.control2.x, segment.control2.y,
          segment.end.x, segment.end.y,
        );
      }
      chunk = [];
    };

    for (const pt of pts) {
      if (!pt) { drawChunk(); continue; }
      chunk.push(pt);
    }
    drawChunk();
    ctx.stroke();

    const head = drawable[drawable.length - 1];
    ctx.beginPath();
    ctx.arc(head.x, head.y, 3, 0, Math.PI * 2);
    ctx.fillStyle = color;
    ctx.fill();
    ctx.strokeStyle = BG;
    ctx.lineWidth = 1;
    ctx.stroke();
    ctx.restore();
  }
}

export default ScopeRenderer;
