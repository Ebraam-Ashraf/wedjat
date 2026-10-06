// Guard the existing chart and SM-grid visual tokens used by the live UI.
// Run with: node verify-dpr.mjs

import { readFileSync } from 'node:fs';

const PASS = [];
const FAIL = [];
const check = (name, condition, extra = '') => (condition ? PASS : FAIL).push(`${name}${extra ? ' — ' + extra : ''}`);
const css = readFileSync(new URL('./src/index.css', import.meta.url), 'utf8');
const chart = readFileSync(new URL('./src/components/TelemetryCharts.jsx', import.meta.url), 'utf8');
const sm = readFileSync(new URL('./src/components/SmEstimate.jsx', import.meta.url), 'utf8');

check('dark dashboard theme remains defined', /--bg:\s*#0a110d/.test(css) && /--panel:\s*#101b14/.test(css));
check('existing text and status colors remain defined', /--green:\s*#77ec8a/.test(css) && /--red:\s*#ff625b/.test(css) && /--cyan:\s*#45d7ed/.test(css));
check('SVG graph retains its existing class hooks', /className="sparkline-grid"/.test(chart) && /className="sparkline-line"/.test(chart) && /className="sparkline-compare"/.test(chart));
check('main chart uses the established green theme token', /\.sparkline-line\s*\{[^}]*stroke:\s*var\(--green\)/s.test(css));
check('comparison line uses the established cyan theme token', /\.sparkline-compare\s*\{[^}]*stroke:\s*var\(--cyan\)/s.test(css));
check('SM estimate keeps its red/green activity colors', /\.sm-active\s*\{[^}]*background:\s*#b32e32/s.test(css) && /\.sm-idle\s*\{[^}]*background:\s*#225732/s.test(css));
check('SM grid remains explicitly an estimate', /Estimated from whole-GPU utilization/.test(sm) && /no per-SM activity data/.test(sm));

console.log('\nPASS');
for (const name of PASS) console.log('  ✓ ' + name);
if (FAIL.length) {
  console.log('\nFAIL');
  for (const name of FAIL) console.log('  ✗ ' + name);
}
console.log(`\n${PASS.length} passed, ${FAIL.length} failed`);
process.exit(FAIL.length ? 1 : 0);
