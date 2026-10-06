import React, { useMemo } from 'react';
import { useApiData } from '../hooks/useApiData.js';
import { MetricChart } from '../components/TelemetryCharts.jsx';
import { formatBytes } from '../api.js';
import SnapshotControls from '../components/SnapshotControls.jsx';

function historySample(row) {
  return {
    t: row.ts,
    util_avg: row.util_gpu_avg,
    util_max: row.util_gpu_max,
    temp: row.temp_max_c,
    mem: row.vram_used_max_bytes,
    power: row.power_mw_sum,
  };
}

export default function History({ gpus, selectedGpu }) {
  const result = useApiData('/api/history?limit=100', { refreshIntervalMs: 20_000 });
  const groups = useMemo(() => {
    const source = result.data || [];
    const filtered = selectedGpu ? source.filter((row) => row.gpu_uuid === selectedGpu) : source;
    const names = new Map(gpus.map((gpu) => [gpu.uuid, gpu.name || `GPU ${gpu.index}`]));
    const byGpu = new Map();
    for (const row of filtered) {
      if (!byGpu.has(row.gpu_uuid)) byGpu.set(row.gpu_uuid, { uuid: row.gpu_uuid, name: names.get(row.gpu_uuid) || row.gpu_name || 'GPU n/a', rows: [] });
      byGpu.get(row.gpu_uuid).rows.push(row);
    }
    return [...byGpu.values()].map((group) => ({ ...group, rows: group.rows.sort((a, b) => a.ts - b.ts), samples: group.rows.map(historySample) }));
  }, [result.data, selectedGpu, gpus]);

  return <div className="page-stack">
    <div className="page-heading"><div><p className="eyebrow">STORED TELEMETRY</p><h1>GPU history</h1><p>1-minute buckets · charted separately per GPU.</p></div><div><span className="source-note">Today UTC · endpoint limit 100 rows</span><SnapshotControls result={result} label="History snapshot" /></div></div>
    <div className="info-strip"><strong>History limits</strong><span>This UI endpoint queries today’s UTC database and returns at most 100 rows total across GPUs. Daily database files are retained for 30 days by default, but older days are not queried on this page. Gaps may reflect unavailable fields or dropped samples.</span></div>
    {result.loading && <div className="panel loading-state">Loading today’s history…</div>}
    {result.error && <div className="inline-error">History could not be loaded: {result.error}</div>}
    {!result.loading && !result.error && groups.length === 0 && <div className="panel empty-state"><h2>No history rows</h2><p>No minute-bucket rows matched this GPU filter. Start the daemon and collect data first.</p></div>}
    {groups.map((group) => <section className="history-device panel" key={group.uuid}>
      <div className="section-title"><div><p className="eyebrow">{group.uuid}</p><h2>{group.name}</h2></div><span>{group.rows.length} returned rows</span></div>
      <div className="chart-grid">
        <MetricChart title="Average utilization" samples={group.samples} field="util_avg" unit="%" maxValue={100} historical />
        <MetricChart title="Maximum utilization" samples={group.samples} field="util_max" unit="%" maxValue={100} historical />
        <MetricChart title="Maximum temperature" samples={group.samples} field="temp" unit=" °C" historical />
        <MetricChart title="Maximum VRAM used" samples={group.samples} field="mem" unit=" MB" scale={1048576} historical />
        <MetricChart title="Power readings sum" samples={group.samples} field="power" unit=" mW" historical />
      </div>
      <div className="table-wrap history-table"><table><thead><tr><th>Minute (UTC)</th><th>Average util</th><th>Max util</th><th>Max temp</th><th>Max VRAM</th><th>Power readings sum</th><th>Samples</th></tr></thead><tbody>
        {[...group.rows].reverse().map((row, index) => <tr key={`${row.ts}:${index}`}><td>{row.time}</td><td>{row.util_gpu_avg == null ? 'n/a' : `${row.util_gpu_avg}%`}</td><td>{row.util_gpu_max == null ? 'n/a' : `${row.util_gpu_max}%`}</td><td>{row.temp_max_c == null ? 'n/a' : `${row.temp_max_c} °C`}</td><td>{row.vram_used_max_bytes == null ? 'n/a' : formatBytes(row.vram_used_max_bytes)}</td><td>{row.power_mw_sum == null ? 'n/a' : `${row.power_mw_sum} mW-samples`}</td><td>{row.n}</td></tr>)}
      </tbody></table></div>
    </section>)}
  </div>;
}
