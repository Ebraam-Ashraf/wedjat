// Canvas 2D scope renderer.
//
// Mental model — exactly what this does:
//
//   Every 0.5s the daemon sends a point. Each point has a real timestamp.
//   Shape-preserving curves pass through the measured points. The line scrolls left at
//   wall-clock speed (60fps via rAF) because x-position is derived from the
//   point's timestamp, not its array index.
//
//   Right edge: the render cursor sits 500ms behind "now" (one sample interval).
//   The value at the cursor is linearly interpolated between the two real samples
//   that bracket it, so the head of the line moves smoothly at 60fps instead of
//   jumping every 500ms. This is interpolation between real measurements, not
//   invented data.
//
//   Left edge: when a point's age reaches 15s it exits the window. The segment
//   connecting it to the next point is clipped precisely at x=left-edge so it
//   slides off smoothly rather than popping off all at once.
//
//   Gap: if the daemon was silent for more than 3s (not just jitter), the line
//   breaks. Normal OS/network jitter of a few hundred ms never breaks it.

import { monotoneBezierSegments } from './curve';

const GRID_LINE = 'rgba(148,163,184,0.58)';
const AXIS_LINE = 'rgba(203,213,225,0.80)';
const LABEL     = 'rgba(226,232,240,0.88)';
const BG        = '#0f172a';

const GUTTER_L = 42;   // px reserved for y-axis labels
const GUTTER_B = 22;   // px reserved for x-axis labels
const LEGEND_H = 22;   // px reserved for the legend above the plot

const MAX_BACKING_PX = 4096;

const SERIES_COLORS = ['#3b82f6', '#8b5cf6', '#10b981', '#f59e0b', '#ec4899', '#06b6d4'];
export const seriesColor = (i) => SERIES_COLORS[i % SERIES_COLORS.length];

export class ScopeRenderer {
  /**
   * @param {HTMLCanvasElement} canvas
   * @param {{
   *   store: object,
   *   field: string,
   *   windowMs: number,
   *   delayMs: number,
   *   yDomain: [number, number],
   *   yTicks: number[],
   *   valueFormat: (v: number) => string,
   *   yFormat?: (v: number) => string,
   *   timeSource?: () => number,
   *   compareField?: string,
   *   compareColor?: string,
   * }} opts
   */
  constructor(canvas, opts) {
    this.canvas  = canvas;
    this.ctx     = canvas.getContext('2d');
    this.opts    = opts;
    this.cssW    = 0;
    this.cssH    = 0;
    this.dpr     = 0;
    this.scale   = 1;
    this._timeSource = opts.timeSource || (() => Date.now());

    // Reused scratch arrays — avoids allocating new arrays every frame.
    this._pts = [];
    this._comparePts = [];

    this._onResize = () => this.resize();
    this._ro = new ResizeObserver(this._onResize);
    this._ro.observe(canvas.parentElement || canvas);
    this.resize();
  }

  destroy() {
    this._ro.disconnect();
  }

  resize() {
    const dpr  = window.devicePixelRatio || 1;
    const rect = this.canvas.getBoundingClientRect();
    const w    = Math.max(1, Math.round(rect.width));
    const h    = Math.max(1, Math.round(rect.height));
    if (w === this.cssW && h === this.cssH && dpr === this.dpr) return;

    this.cssW = w;
    this.cssH = h;
    this.dpr  = dpr;

    // Cap scale so the backing store cannot grow without bound. The CSS height
    // on .scope-canvas must be definite (the clamp() in index.css) — if it
    // isn't, getBoundingClientRect returns the canvas.height attribute which
    // multiplies by dpr on every resize pass.
    const scale = Math.max(0.01, Math.min(dpr, MAX_BACKING_PX / w, MAX_BACKING_PX / h));
    this.scale  = scale;

    this.canvas.width  = Math.round(w * scale);
    this.canvas.height = Math.round(h * scale);
    this.ctx.setTransform(scale, 0, 0, scale, 0, 0);
  }

  // ── geometry ──────────────────────────────────────────────────────────────

  get plot() {
    return {
      x: GUTTER_L,
      y: LEGEND_H,
      w: Math.max(1, this.cssW - GUTTER_L),
      h: Math.max(1, this.cssH - LEGEND_H - GUTTER_B),
    };
  }

  // ── per-frame entry point ─────────────────────────────────────────────────

  draw(now) {
    const { ctx, opts } = this;
    const p = this.plot;

    ctx.clearRect(0, 0, this.cssW, this.cssH);

    const store    = opts.store;
    const windowMs = opts.windowMs;

    // renderT: the timestamp the right edge of the plot represents.
    // Use the configured time source (defaults to Date.now for wall-clock time
    // matching daemon sample timestamps). The rAF 'now' is performance.now()
    // (monotonic) which doesn't match unix ms from the daemon.
    const wallNow  = this._timeSource();
    const renderT  = wallNow - opts.delayMs;
    const winStart = renderT - windowMs;  // left edge timestamp

    this._drawGrid(p, opts, renderT, windowMs);

    const order   = store.gpuOrder;
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
        // Draw compare field (e.g., power_limit) if provided
        if (opts.compareField) {
          const compareColor = opts.compareColor || '#06b6d4';
          this._drawSeries(p, rec, opts.compareField, renderT, winStart, maxGap, compareColor);
        }
      }
    }
  }

  // ── grid (y-axis + x-axis) ────────────────────────────────────────────────

  _drawGrid(p, opts, renderT, windowMs) {
    const { ctx } = this;
    const [lo, hi] = opts.yDomain;

    ctx.save();
    ctx.font = '11px Outfit, sans-serif';

    // — horizontal lines + y labels —
    ctx.textAlign    = 'right';
    ctx.textBaseline = 'middle';
    for (const v of opts.yTicks) {
      const y = Math.round(p.y + p.h - ((v - lo) / (hi - lo)) * p.h) + 0.5;
      ctx.strokeStyle = GRID_LINE;
      ctx.lineWidth   = 1;
      ctx.beginPath();
      ctx.moveTo(p.x,       y);
      ctx.lineTo(p.x + p.w, y);
      ctx.stroke();
      ctx.fillStyle = LABEL;
      ctx.fillText(opts.yFormat ? opts.yFormat(v) : String(v), p.x - 8, y);
    }

    // — vertical lines + x labels —
    // Draw six stable divisions so labels remain legible on a one-minute chart.
    const divisions = 6;
    ctx.textAlign    = 'center';
    ctx.textBaseline = 'top';
    for (let i = 0; i <= divisions; i++) {
      const secondsAgo = Math.round((windowMs - (windowMs / divisions) * i) / 1000);
      const x = Math.round(p.x + (i / divisions) * p.w) + 0.5;
      ctx.strokeStyle = i === divisions ? AXIS_LINE : GRID_LINE;
      ctx.lineWidth   = i === divisions ? 1.5 : 1;
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

  // ── legend ────────────────────────────────────────────────────────────────

  _drawLegend(p, store, order) {
    const { ctx } = this;
    ctx.save();
    ctx.font         = '12px Outfit, sans-serif';
    ctx.textBaseline = 'middle';
    ctx.textAlign    = 'left';

    let x = p.x;
    for (let i = 0; i < order.length; i++) {
      const rec    = store.gpu(order[i]);
      if (!rec) continue;
      const color  = seriesColor(i);
      const latest = this._latestValue(rec, this.opts.field);
      const name   = `GPU ${rec.index}`;
      const val    = latest == null ? '--' : this.opts.valueFormat(latest);

      // Colour swatch
      ctx.fillStyle = color;
      ctx.fillRect(x, 9, 10, 2);

      // GPU name in label colour
      ctx.fillStyle = LABEL;
      ctx.fillText(name, x + 15, 10);
      const nameW = ctx.measureText(name).width;

      // Current value in series colour
      ctx.fillStyle = color;
      ctx.fillText(val, x + 15 + nameW + 6, 10);
      const valW = ctx.measureText(val).width;

      x += 15 + nameW + 6 + valW + 18;
    }
    ctx.restore();
  }

  _drawWatermark(p, text) {
    const { ctx } = this;
    ctx.save();
    ctx.font         = '13px Outfit, sans-serif';
    ctx.fillStyle    = LABEL;
    ctx.textAlign    = 'center';
    ctx.textBaseline = 'middle';
    ctx.fillText(text, p.x + p.w / 2, p.y + p.h / 2);
    ctx.restore();
  }

  // ── series line ───────────────────────────────────────────────────────────

  /**
   * Walk back through the ring and return the newest non-null value for a
   * field. Used for the legend readout so it holds the last real number instead
   * of blanking when the driver skips a report.
   */
  _latestValue(rec, field) {
    const ring = rec.ring;
    for (let i = ring.length - 1; i >= 0; i--) {
      const v = ring[i][field];
      if (v != null && Number.isFinite(v)) return v;
    }
    return null;
  }

  /**
   * Draw one GPU's line for one field.
   *
   * Step by step:
   *
   *  1. Find the two samples that bracket renderT (right edge). Interpolate the
   *     value there so the head moves at 60fps, not 2fps.
   *
   *  2. Find the first sample inside the 15s window. If there's one just outside
   *     the left edge, interpolate where the connecting segment crosses the edge
   *     and start the path there — so the line slides off the left smoothly
   *     instead of popping off point-by-point.
   *
   *  3. Plot every real sample inside the window in timestamp order.
   *
   *  4. Close the path at renderT with the interpolated head value.
   *
   *  5. If two consecutive real samples are more than maxGap apart, lift the pen
   *     (genuine daemon silence, not jitter).
   */
  _drawSeries(p, rec, field, renderT, winStart, maxGap, color) {
    const { ctx }   = this;
    const [lo, hi]  = this.opts.yDomain;
    const span      = hi - lo;
    const scale     = this.opts.scale || 1;
    const pts       = this._pts;
    pts.length      = 0;

    // Map a timestamp to a canvas x-coordinate.
    // winStart → p.x (left edge), renderT → p.x + p.w (right edge).
    const toX = (t) => p.x + ((t - winStart) / (renderT - winStart)) * p.w;

    // Map a value to a canvas y-coordinate, clamped to the domain.
    // Add vertical padding so strokes at 0% and 100% aren't clipped.
    const VERTICAL_PADDING = 2;
    const plotHeight = Math.max(1, p.h - VERTICAL_PADDING * 2);
    const toY = (v) => {
      const scaled = v / scale;
      const c = Math.min(hi, Math.max(lo, scaled));
      return p.y + VERTICAL_PADDING + plotHeight - ((c - lo) / span) * plotHeight;
    };

    const ring = rec.ring;
    const n    = ring.length;
    if (n === 0) return;

    // ── step 1: find startIdx (first sample at or after winStart) ──────────
    // Binary search — this runs every frame so we don't want a linear scan.
    let lo2 = 0, hi2 = n;
    while (lo2 < hi2) {
      const mid = (lo2 + hi2) >> 1;
      if (ring[mid].t < winStart) lo2 = mid + 1;
      else hi2 = mid;
    }
    const startIdx = lo2;

    // ── step 2: find the bracketing samples for the right-edge head ────────
    // prev = last sample at or before renderT
    // next = first sample after renderT
    let prev = startIdx > 0 ? ring[startIdx - 1] : null;
    let next = null;
    for (let i = startIdx; i < n; i++) {
      const s = ring[i];
      if (s.t <= renderT) prev = s;
      else { next = s; break; }
    }

    // No sample old enough to be behind the render cursor yet — happens for the
    // first delayMs after startup. Nothing to draw.
    if (!prev) return;

    // Interpolated value at the right edge (renderT).
    // If there's no following sample, or the gap is too wide, hold the last
    // known value instead of extrapolating into the unknown.
    let headVal = prev[field];
    if (next && next.t - prev.t <= maxGap) {
      const va = prev[field];
      const vb = next[field];
      if (va != null && vb != null) {
        const f = Math.max(0, Math.min(1, (renderT - prev.t) / (next.t - prev.t)));
        headVal = va + (vb - va) * f;
      } else if (va == null) {
        headVal = vb;
      }
    }

    // ── step 3: left-edge clip ─────────────────────────────────────────────
    // The sample just before winStart and the first sample inside the window
    // form a segment. Interpolate where that segment crosses x = winStart and
    // start the path there. This makes the line slide off the left edge
    // continuously instead of the leftmost visible point popping off when it
    // turns 15s old.
    let prevDrawn = null;
    if (startIdx > 0) {
      const outside = ring[startIdx - 1];
      const inside  = ring[startIdx];
      if (
        inside &&
        outside[field] != null && inside[field] != null &&
        inside.t - outside.t <= maxGap
      ) {
        const f     = (winStart - outside.t) / (inside.t - outside.t);
        const vEdge = outside[field] + (inside[field] - outside[field]) * f;
        pts.push({ x: p.x, y: toY(vEdge) });
        prevDrawn = outside;
      }
    }

    // ── step 4: all real samples inside the window ─────────────────────────
    for (let i = startIdx; i < n; i++) {
      const s = ring[i];
      if (s.t > renderT) break;

      const v = s[field];
      // Null means the driver didn't report this field for this sample.
      // Skip it entirely — don't advance prevDrawn, so we don't inflate the
      // gap and accidentally break the line.
      if (v == null) continue;

      // Only break the line on genuine daemon silence (> maxGap), never on
      // normal delivery jitter.
      if (prevDrawn && s.t - prevDrawn.t > maxGap) pts.push(null);

      pts.push({ x: toX(s.t), y: toY(v) });
      prevDrawn = s;
    }

    // ── step 5: right-edge interpolated head ──────────────────────────────
    if (headVal != null && Number.isFinite(headVal)) {
      if (prevDrawn && renderT - prevDrawn.t > maxGap) pts.push(null);
      pts.push({ x: p.x + p.w, y: toY(headVal) });
    }

    // ── draw ──────────────────────────────────────────────────────────────
    const drawable = pts.filter(Boolean);
    if (drawable.length === 0) return;

    ctx.save();
    ctx.strokeStyle = color;
    ctx.lineWidth   = 2;
    ctx.lineJoin    = 'round';
    ctx.lineCap     = 'round';
    ctx.beginPath();

    let chunk = [];
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
      if (!pt) {
        // Null ends this smooth segment at a genuine telemetry gap.
        drawChunk();
        continue;
      }
      chunk.push(pt);
    }
    drawChunk();
    ctx.stroke();

    // Head dot — makes the live tip of the line unambiguous.
    const head = drawable[drawable.length - 1];
    ctx.beginPath();
    ctx.arc(head.x, head.y, 3, 0, Math.PI * 2);
    ctx.fillStyle   = color;
    ctx.fill();
    ctx.strokeStyle = BG;
    ctx.lineWidth   = 1;
    ctx.stroke();

    ctx.restore();
  }
}

export default ScopeRenderer;
