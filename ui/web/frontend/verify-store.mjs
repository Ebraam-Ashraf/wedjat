// Headless verification of the realtime path. Run with: node verify.tmp.mjs
// Stubs just enough DOM for ScopeRenderer; the store needs nothing.

// ── DOM stubs ─────────────────────────────────────────────────────────────
let _now = 1_000_000;
globalThis.performance = { now: () => _now };

class FakeResizeObserver {
  constructor(cb) { this.cb = cb; FakeResizeObserver.instances.push(this); }
  observe() {}
  disconnect() {}
}
FakeResizeObserver.instances = [];
globalThis.ResizeObserver = FakeResizeObserver;
globalThis.window = { devicePixelRatio: 2 };
globalThis.document = { hidden: false };

const PASS = [];
const FAIL = [];
const check = (name, cond, extra = '') => (cond ? PASS : FAIL).push(`${name}${extra ? ' — ' + extra : ''}`);

const { TelemetryStore, LIVE_WINDOW_MS, staleAfterMs } = await import('./src/store/telemetryStore.js');
const { ScopeRenderer } = await import('./src/components/scopeRenderer.js');

// ── helpers ───────────────────────────────────────────────────────────────
function gpuMsg(t, { util = 50, mem = 340, idx = 0, valid = 0b11111110 } = {}) {
  return {
    type: 'gpu',
    timestamp_unix_nano: Math.round(t * 1e6),
    data: [{ Index: idx, UUID: `GPU-${idx}`, UtilGPU: util, MemUsed: mem * 1048576, TempC: 45, PowerMW: 30000, SMClockMHz: 1200, ValidFields: valid }],
  };
}

function fakeCanvas(w = 600, h = 240) {
  const calls = [];
  const rec = (name) => (...a) => calls.push({ name, a });
  return {
    calls,
    width: 0, height: 0,
    getContext: () => ({
      setTransform: rec('setTransform'), clearRect: rec('clearRect'),
      save: rec('save'), restore: rec('restore'),
      beginPath: rec('beginPath'), moveTo: rec('moveTo'), lineTo: rec('lineTo'),
      bezierCurveTo: rec('bezierCurveTo'),
      stroke: rec('stroke'), fill: rec('fill'), arc: rec('arc'),
      fillRect: rec('fillRect'), fillText: rec('fillText'),
      measureText: (t) => ({ width: String(t).length * 7 }),
      set lineWidth(v) {}, set strokeStyle(v) {}, set fillStyle(v) {},
      set lineJoin(v) {}, set lineCap(v) {}, set font(v) {},
      set textAlign(v) {}, set textBaseline(v) {},
    }),
    getBoundingClientRect: () => ({ width: w, height: h }),
    parentElement: null,
  };
}

function makeRenderer(store, field = 'util', yDomain = [0, 100], yTicks = [0, 25, 50, 75, 100]) {
  const canvas = fakeCanvas();
  const r = new ScopeRenderer(canvas, {
    store, field, windowMs: 15000, delayMs: 500, yDomain, yTicks,
    yFormat: (v) => String(v), valueFormat: (v) => String(v),
    timeSource: () => _now,
  });
  return { canvas, r };
}

// ═══════════════════════════════════════════════════════════════════════════
// 1. Time-based sample retention
// ═══════════════════════════════════════════════════════════════════════════
{
  const store = new TelemetryStore();
  // 10 minutes at 2Hz should retain the live window plus a small boundary margin.
  for (let i = 0; i < 1200; i++) {
    _now += 500;
    store.ingest(gpuMsg(_now, { util: i % 100 }));
  }
  const ring = store.gpu(0).ring;
  const span = ring[ring.length - 1].t - ring[0].t;
  check('ring retains the live time window', span >= LIVE_WINDOW_MS && span <= LIVE_WINDOW_MS + 2_000, `span=${span}ms`);
  check('ring size follows timestamps, not a fixed sample cap', ring.length > 120 && ring.length < 140, `len=${ring.length}`);
  check('ring timestamps monotonic', ring.every((s, i) => i === 0 || s.t > ring[i - 1].t));

  const faster = new TelemetryStore();
  for (let i = 0; i < 500; i++) { _now += 250; faster.ingest(gpuMsg(_now)); }
  check('faster cadence retains more time-series samples', faster.gpu(0).ring.length > ring.length, `fast=${faster.gpu(0).ring.length}, slow=${ring.length}`);

  const slower = new TelemetryStore();
  for (let i = 0; i < 100; i++) { _now += 5_000; slower.ingest(gpuMsg(_now)); }
  const slowRing = slower.gpu(0).ring;
  check('slow cadence still retains a full live window', slowRing.at(-1).t - slowRing[0].t >= LIVE_WINDOW_MS, `span=${slowRing.at(-1).t - slowRing[0].t}ms`);
  check('slow cadence history remains bounded by elapsed time', slowRing.length <= 17, `len=${slowRing.length}`);
}

// ═══════════════════════════════════════════════════════════════════════════
// 2. GPU samples are events, including repeated values
// ═══════════════════════════════════════════════════════════════════════════
{
  const store = new TelemetryStore();
  let notifies = 0;
  store.subscribe('gpus', () => notifies++);

  // Repeated readings are still distinct time-series samples.
  for (let i = 0; i < 100; i++) {
    _now += 500;
    store.ingest(gpuMsg(_now, { util: 0, mem: 340 }));
  }
  check('every GPU sample notifies its subscribers', notifies === 100, `notifies=${notifies}`);
  check('repeated readings are retained as separate samples', store.gpu(0).ring.length === 100, `len=${store.gpu(0).ring.length}`);

  const before = notifies;
  _now += 500;
  store.ingest(gpuMsg(_now, { util: 77, mem: 340, idx: 0 }));
  check('a later sample notifies once', notifies === before + 1, `delta=${notifies - before}`);

  // Procs slice: identical list must not notify beyond the first.
  const s2 = new TelemetryStore();
  let pn = 0;
  s2.subscribe('procs', () => pn++);
  const same = { type: 'procs', data: { Procs: [{ PID: 1, GPUUUID: 'g', VRAMBytes: 5 }] } };
  for (let i = 0; i < 50; i++) { _now += 500; s2.ingest(same); }
  check('identical procs notify once (initial only)', pn === 1, `notifies=${pn}`);

  _now += 500;
  s2.ingest({ type: 'procs', data: { Procs: [{ PID: 1, GPUUUID: 'g', VRAMBytes: 6 }] } });
  check('changed procs notify again', pn === 2, `notifies=${pn}`);
}

// ═══════════════════════════════════════════════════════════════════════════
// 3. Interval measurement
// ═══════════════════════════════════════════════════════════════════════════
{
  const store = new TelemetryStore();
  for (let i = 0; i < 30; i++) { _now += 500; store.ingest(gpuMsg(_now)); }
  check('measures 500ms interval', Math.abs(store.stats.avgInterval - 500) < 1, `avg=${store.stats.avgInterval.toFixed(1)}`);
  check('interval window capped at 20', store.stats.intervals.length <= 20, `n=${store.stats.intervals.length}`);

  // A slow daemon must be visible, not hidden.
  const s2 = new TelemetryStore();
  for (let i = 0; i < 30; i++) { _now += 900; s2.ingest(gpuMsg(_now)); }
  check('slow 900ms push is detected', Math.abs(s2.stats.avgInterval - 900) < 1, `avg=${s2.stats.avgInterval.toFixed(1)}`);
  check('maxGap widens for a slow daemon', s2.maxGapMs() > 1000, `maxGap=${s2.maxGapMs().toFixed(0)}`);
  check('stale threshold is at least four observed intervals', staleAfterMs(900) >= 3600, `threshold=${staleAfterMs(900)}`);
  check('five-second cadence does not stale after two seconds', staleAfterMs(5000) === 20_000, `threshold=${staleAfterMs(5000)}`);
  check('unknown cadence uses a conservative startup grace period', staleAfterMs(0) === 20_000, `threshold=${staleAfterMs(0)}`);
  check('per-GPU cadence is measured', s2.gpu(0).intervalMs === 900, `interval=${s2.gpu(0).intervalMs}`);
}

// ═══════════════════════════════════════════════════════════════════════════
// 4. Interval stats over a window (the debug overlay's numbers)
// ═══════════════════════════════════════════════════════════════════════════
{
  const store = new TelemetryStore();
  const deltas = [500, 520, 480, 900, 500, 510];
  for (const d of deltas) { _now += d; store.ingest(gpuMsg(_now)); }
  const s = store.stats;
  // The first message has no predecessor, so it contributes no interval.
  const measured = deltas.slice(1);
  const expectAvg = measured.reduce((a, b) => a + b, 0) / measured.length;
  check('overlay avg matches measured', Math.abs(s.avgInterval - expectAvg) < 1, `${s.avgInterval.toFixed(0)} vs ${expectAvg.toFixed(0)}`);
  check('overlay min is the true min', s.minInterval === Math.min(...measured), `${s.minInterval}`);
  check('overlay max is the true max', s.maxInterval === Math.max(...measured), `${s.maxInterval}`);
}

// ═══════════════════════════════════════════════════════════════════════════
// 5. Interpolation: the head value must advance continuously, not in 2Hz jumps
// ═══════════════════════════════════════════════════════════════════════════
{
  const store = new TelemetryStore();
  // Two real samples 500ms apart: util 0 then 100.
  _now += 500; store.ingest(gpuMsg(_now, { util: 0 }));
  _now += 500; store.ingest(gpuMsg(_now, { util: 100 }));

  const { r } = makeRenderer(store);
  const FRAME = 1000 / 60;

  // Walk rAF frames across the interval where both samples bracket renderT.
  const values = [];
  let t = _now;
  for (let i = 0; i < 40; i++) {
    t += FRAME;
    _now = t;
    r.draw(t);
    values.push(lastHeadValue(r));
  }

  const first = values[0];
  const last = values[values.length - 1];
  check('head value interpolates (not stuck)', Math.abs(last - first) > 0.5, `${first.toFixed(2)} -> ${last.toFixed(2)}`);

  // Every step must be small and uniform: 100 units over 500ms at 60fps is
  // ~3.3 units per frame. A 2Hz staircase would show a 100-unit step somewhere.
  let maxStep = 0;
  for (let i = 1; i < values.length; i++) {
    maxStep = Math.max(maxStep, Math.abs(values[i] - values[i - 1]));
  }
  check('no 2Hz step in interpolated head', maxStep < 10, `maxStep=${maxStep.toFixed(2)} units/frame`);

  const monotonic = values.every((v, i) => i === 0 || v >= values[i - 1] - 1e-9);
  check('interpolation is monotonic between samples', monotonic);

  const bounded = values.every((v) => v >= -0.001 && v <= 100.001);
  check('interpolated value stays inside the axis', bounded);
}

function lastHeadValue(r) {
  // The head marker is the arc() call; recover its y and invert valY.
  const arcs = r.canvas.calls.filter((c) => c.name === 'arc');
  if (!arcs.length) return NaN;
  const y = arcs[arcs.length - 1].a[1];
  const p = r.plot;
  return ((p.y + p.h - y) / p.h) * 100;
}

// ═══════════════════════════════════════════════════════════════════════════
// 6. x positions derive from timestamps and scroll at constant velocity
// ═══════════════════════════════════════════════════════════════════════════
{
  const store = new TelemetryStore();
  const base = _now;
  for (let i = 0; i < 30; i++) { _now += 500; store.ingest(gpuMsg(_now, { util: 50 })); }

  const { r } = makeRenderer(store);
  const p = r.plot;

  // Track one fixed sample's x across successive frames. The series polyline is
  // the path before the head marker's own beginPath, so step back past that.
  const seriesPathStart = (calls, arcIdx) => {
    let i = arcIdx;
    // First back past the head marker's beginPath.
    for (; i >= 0; i--) if (calls[i].name === 'beginPath') break;
    // Then back to the polyline's beginPath.
    for (i -= 1; i >= 0; i--) if (calls[i].name === 'beginPath') return i;
    return 0;
  };
  const seriesFirstX = (calls) => {
    const arcIdx = calls.map((c) => c.name).lastIndexOf('arc');
    if (arcIdx < 0) return NaN;
    const begin = seriesPathStart(calls, arcIdx);
    for (let i = begin; i < arcIdx; i++) {
      if (calls[i].name === 'moveTo' || calls[i].name === 'lineTo') return calls[i].a[0];
    }
    return NaN;
  };

  const xs = [];
  let t = _now;
  for (let i = 0; i < 12; i++) {
    t += 1000 / 60;
    _now = t;
    r.canvas.calls.length = 0;
    r.draw(t);
    xs.push(seriesFirstX(r.canvas.calls));
  }

  const steps = xs.slice(1).map((x, i) => x - xs[i]);
  const spread = Math.max(...steps) - Math.min(...steps);
  // Constant velocity means every step is identical. Any spread means the x
  // position is snapping to indices instead of following the clock.
  check('x scrolls at constant velocity', spread < 0.05, `step spread=${spread.toFixed(4)}px`);
  check('x moves left as time advances', steps[0] < 0, `step=${steps[0].toFixed(3)}px`);

  // The 15s window should take 15s to traverse the plot.
  const pxPerSec = Math.abs(steps[0]) * 60;
  const expected = p.w / 15;
  check('traverse time equals the 15s window', Math.abs(pxPerSec - expected) < 1, `${pxPerSec.toFixed(1)} vs ${expected.toFixed(1)} px/s`);

  // Whole window must be drawn, not just the newest sample or two. The renderer
  // uses Bezier segments, so count those instead of obsolete lineTo calls.
  const last = r.canvas.calls;
  const arcIdx2 = last.map((c) => c.name).lastIndexOf('arc');
  const begin2 = seriesPathStart(last, arcIdx2);
  const seriesOps = last.slice(begin2, arcIdx2).filter((c) =>
    c.name === 'moveTo' || c.name === 'lineTo' || c.name === 'bezierCurveTo'
  ).length;
  check('full window is plotted (29 samples + interpolated head)', seriesOps >= 28, `ops=${seriesOps}`);
}

// ═══════════════════════════════════════════════════════════════════════════
// 7. Degenerate inputs must not crash or draw nonsense
// ═══════════════════════════════════════════════════════════════════════════
{
  const empty = new TelemetryStore();
  const { r } = makeRenderer(empty);
  let threw = null;
  try { r.draw(_now); } catch (e) { threw = e; }
  check('empty store renders without throwing', threw === null, threw && threw.message);

  const store = new TelemetryStore();
  _now += 500; store.ingest(gpuMsg(_now, { util: 42 }));
  const { r: r1 } = makeRenderer(store);
  threw = null;
  try { r1.draw(_now); } catch (e) { threw = e; }
  check('single sample renders without throwing', threw === null, threw && threw.message);
  // With a 500ms render delay a brand new sample is not yet old enough to plot,
  // so an empty plot here is correct: the legend still shows the value.
  const onePoint = r1.canvas.calls.filter((c) => c.name === 'arc').length;
  check('sample younger than the delay is not plotted', onePoint === 0, `arcs=${onePoint}`);

  // Once it is old enough it must appear.
  _now += 600;
  r1.draw(_now);
  check('sample plots once past the delay', r1.canvas.calls.filter((c) => c.name === 'arc').length >= 1);

  // All fields invalid: must draw no line at all rather than dropping to 0.
  const store2 = new TelemetryStore();
  for (let i = 0; i < 10; i++) { _now += 500; store2.ingest(gpuMsg(_now, { util: 0, valid: 0 })); }
  const { r: r2 } = makeRenderer(store2);
  r2.draw(_now);
  // The series head marker is the only arc; gridlines never arc.
  check('all-invalid data draws no line', r2.canvas.calls.filter((c) => c.name === 'arc').length === 0);
}

// ═══════════════════════════════════════════════════════════════════════════
// 8. Gap handling: a long stall must break the line, not bridge it
// ═══════════════════════════════════════════════════════════════════════════
{
  const store = new TelemetryStore();
  for (let i = 0; i < 5; i++) { _now += 500; store.ingest(gpuMsg(_now, { util: 20 })); }
  // 6 second stall, then resume.
  _now += 6000;
  for (let i = 0; i < 5; i++) { _now += 500; store.ingest(gpuMsg(_now, { util: 80 })); }

  const { r } = makeRenderer(store);
  r.canvas.calls.length = 0;
  r.draw(_now);

  // A break means an extra moveTo: 5 real points after the stall, each segment
  // restarted, so moveTo count must exceed 2.
  const moveTos = r.canvas.calls.filter((c) => c.name === 'moveTo').length;
  check('stall breaks the line into segments', moveTos > 2, `moveTo=${moveTos}`);
}

// ═══════════════════════════════════════════════════════════════════════════
// 9. Device pixel ratio and resize
// ═══════════════════════════════════════════════════════════════════════════
{
  const store = new TelemetryStore();
  _now += 500; store.ingest(gpuMsg(_now));
  const { canvas, r } = makeRenderer(store);
  check('backing store scaled by DPR', canvas.width === 1200 && canvas.height === 480, `${canvas.width}x${canvas.height}`);

  const tf = canvas.calls.find((c) => c.name === 'setTransform');
  check('context pre-scaled so drawing uses CSS pixels', tf && tf.a[0] === 2, JSON.stringify(tf && tf.a));

  // Resize must resize the backing store again.
  canvas.getBoundingClientRect = () => ({ width: 400, height: 300 });
  r.resize();
  check('resize updates backing store', canvas.width === 800 && canvas.height === 600, `${canvas.width}x${canvas.height}`);
}

// ═══════════════════════════════════════════════════════════════════════════
// 10. Multi-GPU: one line each, distinct colours
// ═══════════════════════════════════════════════════════════════════════════
{
  const store = new TelemetryStore();
  for (let i = 0; i < 10; i++) {
    _now += 500;
    store.ingest({
      type: 'gpu',
      timestamp_unix_nano: Math.round(_now * 1e6),
      data: [
        { Index: 0, UUID: 'GPU-0', UtilGPU: 10, MemUsed: 100 * 1048576, TempC: 40, PowerMW: 1000, SMClockMHz: 100, ValidFields: 0b11111110 },
        { Index: 1, UUID: 'GPU-1', UtilGPU: 90, MemUsed: 900 * 1048576, TempC: 80, PowerMW: 9000, SMClockMHz: 900, ValidFields: 0b11111110 },
      ],
    });
  }
  check('two GPUs tracked', store.gpuOrder.length === 2, `n=${store.gpuOrder.length}`);

  const { r } = makeRenderer(store);
  r.draw(_now);
  const headArcs = r.canvas.calls.filter((c) => c.name === 'arc');
  check('one head marker per GPU', headArcs.length === 2, `arcs=${headArcs.length}`);

  // Distinct y: GPU 0 at 10% and GPU 1 at 90% must not overlap.
  const ys = headArcs.map((a) => a.a[1]).sort((a, b) => a - b);
  check('GPU lines at distinct heights', Math.abs(ys[1] - ys[0]) > 50, `ys=${ys.map((y) => y.toFixed(0))}`);

  // VRAM axis 0..1024 must place 100MB low and 900MB high.
  const { r: rv } = makeRenderer(store, 'mem', [0, 1024], [0, 256, 512, 768, 1024]);
  rv.draw(_now);
  const memYs = rv.canvas.calls.filter((c) => c.name === 'arc').map((a) => a.a[1]).sort((a, b) => a - b);
  const p = rv.plot;
  check('900MB sits high on 0-1024 axis', memYs[0] < p.h * 0.25, `y=${memYs[0].toFixed(0)} of ${p.h}`);
  check('100MB sits low on 0-1024 axis', memYs[1] > p.h * 0.75, `y=${memYs[1].toFixed(0)} of ${p.h}`);
}

// ═══════════════════════════════════════════════════════════════════════════
console.log('\nPASS');
for (const p of PASS) console.log('  ✓ ' + p);
if (FAIL.length) {
  console.log('\nFAIL');
  for (const f of FAIL) console.log('  ✗ ' + f);
}
console.log(`\n${PASS.length} passed, ${FAIL.length} failed`);
process.exit(FAIL.length ? 1 : 0);
