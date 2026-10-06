// Verify that active live charts use one shared render loop without React frame updates.
// Run with: node verify-loop.mjs

import fs from 'fs';

const PASS = [];
const FAIL = [];
const check = (name, condition, extra = '') => (condition ? PASS : FAIL).push(`${name}${extra ? ' — ' + extra : ''}`);
const read = (file) => fs.readFileSync(file, 'utf8');

const chart = read('src/components/TelemetryCharts.jsx');
check('active charts subscribe to the shared RAF scheduler', /rafLoop\.add/.test(chart) && /from '\.\.\/store\/rafLoop(?:\.js)?'/.test(chart));
const scheduler = read('src/store/rafLoop.js');
check('shared RAF scheduler stops after its last chart unsubscribes', /this\.subs\.delete\(fn\);\s*this\._sync\(\);/.test(scheduler));
check('charts do not create their own animation-frame loop', !/requestAnimationFrame|cancelAnimationFrame/.test(chart));
check('charts do not run local clock timers', !/setInterval|setTimeout/.test(chart));
check('frame updates write SVG paths without React state', /setAttribute\('d'/.test(chart) && !/setState|useState/.test(chart));
check('live chart inputs include measured cadence', /cadenceMeasured/.test(chart) && /intervalMs/.test(chart));
check('latest value remains a real sample', /latestValue\(samples, field\)/.test(chart));
check('chart exposes visual cursor delay to users', /delayed visual cursor/.test(chart) && /data-visual-delay-ms/.test(chart));

const paths = read('src/components/chartPaths.js');
check('render cursor is based on observed sample cadence', /interval \* 1\.2/.test(paths));
check('chart paths split at long timestamp gaps', /point\.t - previousTime > maxGapMs/.test(paths));
check('cursor interpolation requires real samples on both sides', /before && after/.test(paths) && /after\.t - before\.t <= maxGapMs/.test(paths));
check('chart does not synthesize a held endpoint', !/series\.push\(\{\s*t:\s*now/.test(paths));

const store = read('src/store/telemetryStore.js');
check('socket GPU messages notify the GPU slice for each sample', /this\._notify\('gpus'\)/.test(store));
check('GPU samples are pruned by elapsed time', /retentionMs\s*=\s*LIVE_WINDOW_MS/.test(store));
check('freshness threshold uses the observed interval', /interval \* 4/.test(store));
check('other socket slices remain separately ingested', /_ingestProcs|_ingestAggs|_ingestEvent/.test(store));

const app = read('src/App.jsx');
check('app does not tick React state continuously for graph time', !/setFreshnessNow|requestAnimationFrame/.test(app));
check('app creates one shared telemetry store', /useMemo\(\(\) => new TelemetryStore\(\)/.test(app));

const dashboard = read('src/pages/Dashboard.jsx');
check('dashboard still renders the SM estimate', /<SmEstimate\b/.test(dashboard));
check('dashboard passes measured cadence into the live chart', /intervalMs=\{sample\.intervalMs\}/.test(dashboard) && /cadenceMeasured=\{sample\.cadenceMeasured\}/.test(dashboard));

console.log('\nPASS');
for (const name of PASS) console.log('  ✓ ' + name);
if (FAIL.length) {
  console.log('\nFAIL');
  for (const name of FAIL) console.log('  ✗ ' + name);
}
console.log(`\n${PASS.length} passed, ${FAIL.length} failed`);
process.exit(FAIL.length ? 1 : 0);
