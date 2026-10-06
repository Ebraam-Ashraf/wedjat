// End-to-end check of socket samples flowing into truthful, timestamped chart paths.
// Run with: node verify-e2e.mjs

globalThis.performance = { now: () => 1_000 };

const { TelemetryStore } = await import('./src/store/telemetryStore.js');
const { latestValue, pathFor, renderCursorTime } = await import('./src/components/chartPaths.js');

const PASS = [];
const FAIL = [];
const check = (name, condition, extra = '') => (condition ? PASS : FAIL).push(`${name}${extra ? ' — ' + extra : ''}`);

function gpuMessage(timestampMs, entries) {
  return {
    type: 'gpu',
    timestamp_unix_nano: Math.round(timestampMs * 1e6),
    data: entries.map(({ index, util, valid = 0b11111110 }) => ({
      Index: index,
      UUID: `GPU-${index}`,
      UtilGPU: util,
      MemUsed: 256 * 1048576,
      TempC: 45,
      PowerMW: 30_000,
      SMClockMHz: 1200,
      ValidFields: valid,
    })),
  };
}

const store = new TelemetryStore();
let gpuNotifications = 0;
let processNotifications = 0;
store.subscribe('gpus', () => gpuNotifications++);
store.subscribe('procs', () => processNotifications++);

store.ingest(gpuMessage(10_000, [{ index: 0, util: 10 }, { index: 1, util: 90 }]));
performance.now = () => 1_500;
store.ingest(gpuMessage(10_500, [{ index: 0, util: 10 }, { index: 1, util: 90 }]));

check('socket GPU samples create per-GPU histories', store.gpu(0).ring.length === 2 && store.gpu(1).ring.length === 2);
check('identical readings remain separate timestamped points', store.gpu(0).ring[0].util === store.gpu(0).ring[1].util && store.gpu(0).ring[1].t > store.gpu(0).ring[0].t);
check('each GPU message notifies the GPU slice', gpuNotifications === 2, `notifications=${gpuNotifications}`);
store.ingest({ type: 'procs', data: { Procs: [] } });
check('process events notify only the process slice', processNotifications === 1 && gpuNotifications === 2);

const livePaths = pathFor(store.gpu(0).ring, 'util', 320, 64, 100, 1, 10_500, 1_000, 1_000);
check('live path uses actual sample timestamps and stays inside the vertical stroke bounds', livePaths.length === 1 && livePaths[0].includes('M160.0,56.0') && livePaths[0].includes('L320.0,56.0'), livePaths.join(' | '));
const edgePaths = pathFor([{ t: 0, util: 0 }, { t: 1_000, util: 100 }], 'util', 320, 64, 100, 1, 1_000, 1_000, 1_000);
check('0% and 100% stay inside the SVG viewBox instead of clipping', edgePaths.length === 1 && edgePaths[0].includes('M0.0,62.0') && edgePaths[0].includes('L320.0,2.0') && !/,0\.0|,64\.0/.test(edgePaths[0]), edgePaths.join(' | '));

const cursorSamples = [
  { t: 10_000, util: 0 },
  { t: 10_500, util: 100 },
];
const cursorTime = renderCursorTime(10_800, 500, true);
const cursorPath = pathFor(cursorSamples, 'util', 320, 64, 100, 1, cursorTime, 1_000, 1_000);
check('500ms feed uses a 600ms delayed render cursor', cursorTime === 10_200);
check('delayed cursor interpolates between bracketing real samples', cursorPath.length === 1 && cursorPath[0].includes('L320.0,38.0'), cursorPath.join(' | '));
check('five-second feed reports its corresponding visualization delay', 111_000 - renderCursorTime(111_000, 5_000, true) === 6_000);
check('unknown cadence starts with the 500ms default delay', 11_100 - renderCursorTime(11_100, 5_000, false) === 600);
const beyondLatest = pathFor(cursorSamples, 'util', 320, 64, 100, 1, 20_000, 20_000, 1_000);
check('cursor does not extrapolate beyond the newest real sample', beyondLatest.length === 1 && !beyondLatest[0].includes('L320.0,'));

performance.now = () => 2_000;
store.ingest(gpuMessage(13_000, [{ index: 0, util: 80 }, { index: 1, util: 20 }]));
const gapPaths = pathFor(store.gpu(0).ring, 'util', 320, 64, 100, 1, 13_000, 60_000, 1_000);
check('missed-sample gap creates separate path segments', gapPaths.length === 2, `paths=${gapPaths.length}`);

performance.now = () => 2_500;
store.ingest(gpuMessage(13_500, [{ index: 0, util: null, valid: 0 }, { index: 1, util: 30 }]));
const nullPaths = pathFor(store.gpu(0).ring, 'util', 320, 64, 100, 1, 13_500, 60_000, 1_000);
check('invalid readings are represented as line breaks, not zero', nullPaths.length === 2 && !nullPaths.some((path) => /,64\.0/.test(path)));
check('latest metric value is n/a when the newest sample is invalid', latestValue(store.gpu(0).ring, 'util') === null);
check('second GPU data remains independent', store.gpu(1).ring.at(-1).util === 30);

const slowStore = new TelemetryStore();
performance.now = () => 3_000;
slowStore.ingest(gpuMessage(100_000, [{ index: 0, util: 20 }]));
performance.now = () => 8_000;
slowStore.ingest(gpuMessage(105_000, [{ index: 0, util: 80 }]));
check('five-second socket feed cadence is measured from browser receipt', slowStore.gpu(0).intervalMs === 5_000 && slowStore.stats.avgInterval === 5_000);
check('five-second sample timestamps remain five seconds apart', slowStore.gpu(0).ring[1].t - slowStore.gpu(0).ring[0].t === 5_000);
check('five-second cadence uses a twenty-second stale threshold', slowStore.gpu(0).freshnessThresholdMs === 20_000);

console.log('\nPASS');
for (const name of PASS) console.log('  ✓ ' + name);
if (FAIL.length) {
  console.log('\nFAIL');
  for (const name of FAIL) console.log('  ✗ ' + name);
}
console.log(`\n${PASS.length} passed, ${FAIL.length} failed`);
process.exit(FAIL.length ? 1 : 0);
