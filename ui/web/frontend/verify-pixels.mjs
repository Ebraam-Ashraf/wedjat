// Rasterises the real ScopeRenderer into a pixel buffer and reports what colour
// actually lands on screen at each grid line.
//
// The other verify scripts use a context that RECORDS calls. That proves the
// drawing commands are issued but says nothing about whether any pixel is
// painted or what colour it ends up. This one composites for real, so a grid
// that is too faint to see shows up as a low contrast number here.
//
// Run with: node verify-pixels.mjs

import { readFileSync } from 'node:fs';

const rendererSrc = readFileSync(new URL('./src/components/scopeRenderer.js', import.meta.url), 'utf8');
const css = readFileSync(new URL('./src/index.css', import.meta.url), 'utf8');

const PASS = [];
const FAIL = [];
const check = (n, c, e = '') => (c ? PASS : FAIL).push(`${n}${e ? ' — ' + e : ''}`);

// ── colour helpers ────────────────────────────────────────────────────────
function parseColor(s) {
  if (s.startsWith('#')) {
    const h = s.replace('#', '');
    return { rgb: [parseInt(h.slice(0, 2), 16), parseInt(h.slice(2, 4), 16), parseInt(h.slice(4, 6), 16)], a: 1 };
  }
  const m = s.match(/rgba?\(([^)]+)\)/);
  const p = m[1].split(',').map(parseFloat);
  return { rgb: [p[0], p[1], p[2]], a: p.length > 3 ? p[3] : 1 };
}
function over(fg, a, bg) {
  return [fg[0] * a + bg[0] * (1 - a), fg[1] * a + bg[1] * (1 - a), fg[2] * a + bg[2] * (1 - a)];
}
function lum([r, g, b]) {
  const f = (c) => { const v = c / 255; return v <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4; };
  return 0.2126 * f(r) + 0.7152 * f(g) + 0.0722 * f(b);
}
function contrast(a, b) {
  const x = lum(a); const y = lum(b);
  return (Math.max(x, y) + 0.05) / (Math.min(x, y) + 0.05);
}
const fmt = (c) => `rgb(${c.map((v) => Math.round(v)).join(',')})`;

// The real backdrop chain: the canvas is never filled, so whatever is behind it
// in the page is what the grid is composited onto.
const bodyHex = (css.match(/--bg-dark:\s*(#[0-9a-f]{6})/i) || [, '#0f172a'])[1];
const panelDecl = (css.match(/--bg-panel:\s*rgba\(([^)]+)\)/i) || [, '30,41,59,0.7'])[1].split(',').map(parseFloat);
const bodyHexArr = parseColor(bodyHex).rgb;
const BACKDROP = over([panelDecl[0], panelDecl[1], panelDecl[2]], panelDecl[3], bodyHexArr);

// ── a rasterising 2D context ──────────────────────────────────────────────
// Only the operations ScopeRenderer actually uses. Strokes are rasterised as
// axis-aligned rectangles, which is what every grid line is.
function makeRasterCtx(buf, W, H, scale) {
  const state = { strokeStyle: '#000', fillStyle: '#000', lineWidth: 1 };
  const stack = [];
  let path = [];
  const put = (x, y, col, alpha) => {
    const px = Math.round(x); const py = Math.round(y);
    if (px < 0 || py < 0 || px >= W || py >= H) return;
    const i = (py * W + px) * 3;
    buf[i] = col[0] * alpha + buf[i] * (1 - alpha);
    buf[i + 1] = col[1] * alpha + buf[i + 1] * (1 - alpha);
    buf[i + 2] = col[2] * alpha + buf[i + 2] * (1 - alpha);
  };
  const ctx = {
    _scale: scale,
    setTransform(s) { scale = s; },
    save() { stack.push({ ...state }); },
    restore() { Object.assign(state, stack.pop() || {}); },
    set strokeStyle(v) { state.strokeStyle = v; },
    get strokeStyle() { return state.strokeStyle; },
    set fillStyle(v) { state.fillStyle = v; },
    get fillStyle() { return state.fillStyle; },
    set lineWidth(v) { state.lineWidth = v; },
    get lineWidth() { return state.lineWidth; },
    beginPath() { path = []; },
    moveTo(x, y) { path.push([x * scale, y * scale]); },
    lineTo(x, y) { path.push([x * scale, y * scale]); },
    arc(x, y, r) { path.push([x * scale, y * scale]); },
    clearRect() {},
    fillText() {},
    measureText: (t) => ({ width: String(t).length * 7 }),
    fill() {},
    fillRect(x, y, w, h) {
      const c = parseColor(state.fillStyle);
      for (let yy = 0; yy < h * scale; yy++) {
        for (let xx = 0; xx < w * scale; xx++) put(x * scale + xx, y * scale + yy, c.rgb, c.a);
      }
    },
    stroke() {
      const c = parseColor(state.strokeStyle);
      const halfW = Math.max(1, Math.round(state.lineWidth * scale)) / 2;
      for (let i = 1; i < path.length; i++) {
        const [x0, y0] = path[i - 1];
        const [x1, y1] = path[i];
        const steps = Math.ceil(Math.max(Math.abs(x1 - x0), Math.abs(y1 - y0))) * 2 + 1;
        for (let s = 0; s <= steps; s++) {
          const x = x0 + ((x1 - x0) * s) / steps;
          const y = y0 + ((y1 - y0) * s) / steps;
          for (let oy = -Math.floor(halfW); oy <= Math.floor(halfW); oy++) {
            for (let ox = -Math.floor(halfW); ox <= Math.floor(halfW); ox++) {
              put(x + ox, y + oy, c.rgb, c.a);
            }
          }
        }
      }
    },
  };
  return ctx;
}

// ── build the renderer against the raster context ─────────────────────────
let _now = 0;
globalThis.performance = { now: () => _now };
class RO { observe() {} disconnect() {} }
globalThis.ResizeObserver = RO;
globalThis.window = { devicePixelRatio: 1 };

const CSS_W = 820;
const CSS_H = 240;                 // matches RealtimeChart's inline height
const storeMod = await import('./src/store/telemetryStore.js');
const store = new storeMod.TelemetryStore();

const rendererMod = await import('./src/components/scopeRenderer.js');
const { ScopeRenderer } = rendererMod;

const canvas = {
  width: 0, height: 0,
  parentElement: null,
  _ctx: null,
  getContext() {
    if (!this._ctx) this._ctx = makeRasterCtx(this._buf, this.width, this.height, 1);
    return this._ctx;
  },
  getBoundingClientRect: () => ({ width: CSS_W, height: CSS_H }),
};
canvas._buf = new Float64Array(CSS_W * CSS_H * 3);
// Pre-fill with the page backdrop, since the canvas is never filled.
for (let i = 0; i < CSS_W * CSS_H; i++) {
  canvas._buf[i * 3] = BACKDROP[0];
  canvas._buf[i * 3 + 1] = BACKDROP[1];
  canvas._buf[i * 3 + 2] = BACKDROP[2];
}

const renderer = new ScopeRenderer(canvas, {
  store,
  field: 'util',
  windowMs: 15000,
  delayMs: 500,
  yDomain: [0, 100],
  yTicks: [0, 25, 50, 75, 100],
  yFormat: (v) => String(v),
  valueFormat: (v) => `${Math.round(v)}%`,
});

// Feed 40 samples at the real 500ms cadence so the window is full.
let base = 1000;
for (let i = 0; i < 40; i++) {
  store.ingest({
    type: 'gpu',
    timestamp_unix_nano: (base + i * 500) * 1e6,
    data: [{ Id: 0, TsNano: (base + i * 500) * 1e6, UtilGPU: 30 + Math.round(25 * Math.sin(i / 3)), TempC: 45, PowerMW: 20000, VramUsedBytes: 500, SMClockMHz: 1200 }],
  });
}
_now = base + 40 * 500;
renderer.draw(_now);

const p = renderer.plot;
const at = (x, y) => {
  const px = Math.round(x); const py = Math.round(y);
  const i = (py * CSS_W + px) * 3;
  return [canvas._buf[i], canvas._buf[i + 1], canvas._buf[i + 2]];
};

// ── the y grid (horizontal lines) ─────────────────────────────────────────
const midX = Math.round(p.x + p.w / 2);
const yRows = [100, 75, 50, 25, 0].map((v) => Math.round(p.y + p.h - (v / 100) * p.h) + 0.5);
console.log('Y grid (horizontal lines), sampled at x=%d:', midX);
const yResults = yRows.map((y) => {
  const c = at(midX, y);
  return { y, c, k: contrast(c, BACKDROP) };
});
for (const r of yResults) {
  console.log(`  y=${String(r.y).padStart(4)}  pixel=${fmt(r.c).padEnd(18)} contrast vs panel ${r.k.toFixed(2)}:1`);
}
const worstY = Math.min(...yResults.map((r) => r.k));
check('every y grid line is painted inside the canvas',
  yResults.every((r) => r.y > 0 && r.y < CSS_H), `ys=${yRows.join(',')} of ${CSS_H}`);
check('y grid lines differ from the backdrop', yResults.every((r) => r.k > 1.001),
  `min ${worstY.toFixed(2)}:1`);
check('y grid is visible, not just present', worstY >= 2.5, `worst ${worstY.toFixed(2)}:1`);

// ── the x grid (vertical lines) ───────────────────────────────────────────
const midY = Math.round(p.y + p.h / 2);
const cols = Math.round(15000 / 500);
const xCols = [];
for (let i = 0; i <= cols; i++) {
  xCols.push(Math.round(p.x + (i / cols) * p.w) + 0.5);
}
console.log('\nX grid (vertical lines), sampled at y=%d:', midY);
const xResults = [];
for (const x of xCols) {
  const c = at(x, midY);
  xResults.push({ x, c, k: contrast(c, BACKDROP) });
}
const shown = xResults.slice(0, 4).concat(xResults.slice(-2));
for (const r of shown) {
  console.log(`  x=${String(r.x).padStart(4)}  pixel=${fmt(r.c).padEnd(18)} contrast vs panel ${r.k.toFixed(2)}:1`);
}
const inBounds = xResults.filter((r) => r.x >= 0 && r.x < CSS_W);
const worstX = Math.min(...xResults.map((r) => r.k));
check('every x grid line is painted inside the canvas',
  inBounds.length >= xResults.length - 1,
  `${inBounds.length}/${xResults.length} in bounds (the last sits on the right edge)`);
check('x grid lines differ from the backdrop', xResults.every((r) => r.k > 1.001),
  `min ${worstX.toFixed(2)}:1`);
check('x grid is visible, not just present', worstX >= 2.5, `worst ${worstX.toFixed(2)}:1`);

// ── what the previous values actually produced ────────────────────────────
// Re-rasterise the same frame with the old grid colours to show the difference.
function contrastFor(colorStr) {
  const c = parseColor(colorStr);
  return contrast(over(c.rgb, c.a, BACKDROP), BACKDROP);
}
const original = contrastFor('rgba(255,255,255,0.07)');
const firstAttempt = contrastFor('rgba(148,163,184,0.24)');
const current = contrastFor((rendererSrc.match(/const GRID_LINE = '([^']+)'/) || [, ''])[1]);
console.log('\nGrid colour against the real panel backdrop:');
console.log(`  original rgba(255,255,255,0.07)      ${original.toFixed(2)}:1`);
console.log(`  first fix rgba(148,163,184,0.24)     ${firstAttempt.toFixed(2)}:1`);
console.log(`  current  ${(rendererSrc.match(/const GRID_LINE = '([^']+)'/) || [, ''])[1].padEnd(28)} ${current.toFixed(2)}:1`);
check('the original grid was imperceptible', original < 1.5, `${original.toFixed(2)}:1`);
check('the first fix was still imperceptible', firstAttempt < 2.0, `${firstAttempt.toFixed(2)}:1`);
check('the current grid is visible', current >= 2.5, `${current.toFixed(2)}:1`);

console.log('\nPASS');
for (const p of PASS) console.log('  ✓ ' + p);
if (FAIL.length) {
  console.log('\nFAIL');
  for (const f of FAIL) console.log('  ✗ ' + f);
}
console.log(`\n${PASS.length} passed, ${FAIL.length} failed`);
process.exit(FAIL.length ? 1 : 0);