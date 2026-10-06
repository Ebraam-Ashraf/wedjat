import React, { useMemo, useState } from 'react';
import { useStoreValue } from '../hooks/useStoreValue';
import { useApiData } from '../hooks/useApiData';
import { API_NAMES, formatBytes } from '../gpuData';
import SnapshotControls from '../components/SnapshotControls';

function LiveAggDetails({ pid, liveRows, historyRows }) {
  const live = liveRows.filter((row) => Number(row.Tgid) === Number(pid));
  const history = historyRows.filter((row) => Number(row.tgid) === Number(pid));
  return <div className="expanded-content">
    <h4>Recent live API counters</h4>
    {live.length ? <div className="table-wrap"><table><thead><tr><th>API</th><th>CUDA ordinal</th><th>Count</th><th>Bytes</th><th>Latency sum</th><th>Latency max</th><th>Alloc bytes</th><th>Free bytes</th><th>Errors</th><th>UVM faults</th><th>UVM evictions</th></tr></thead><tbody>
      {live.map((row, index) => <tr key={`${row.ApiID}:${row.Ordinal}:${index}`}><td>{API_NAMES[row.ApiID] || `API ${row.ApiID}`}</td><td>{row.Ordinal === 0xffffffff ? 'unknown' : row.Ordinal}</td><td>{row.Count}</td><td>{formatBytes(row.Bytes)}</td><td>{row.LatencySumNs} ns</td><td>{row.LatencyMaxNs} ns</td><td>{formatBytes(row.AllocBytes)}</td><td>{formatBytes(row.FreeBytes)}</td><td>{row.Errors}</td><td>{row.UvmFaults}</td><td>{row.UvmEvicts}</td></tr>)}
    </tbody></table></div> : <p className="empty-inline">No live aggregate rows for this process yet.</p>}
    <p className="muted compact-note">CUDA ordinals are process-visible indices. Live aggregate messages contain no GPU UUID, so an ordinal is not always safely mappable to a physical GPU.</p>
    <h4>Stored minute totals (today UTC)</h4>
    {history.length ? <div className="table-wrap"><table><thead><tr><th>Minute</th><th>GPU</th><th>Launches</th><th>Memcpy calls / bytes</th><th>Alloc calls / bytes</th><th>Free bytes</th><th>Sync calls / sum / max</th><th>Ioctl</th><th>UVM faults / evictions</th><th>Errors</th></tr></thead><tbody>{history.map((row, index) => <tr key={`${row.ts}:${row.gpu_name}:${index}`}><td>{row.time}</td><td>{row.gpu_name || 'n/a'}</td><td>{row.launches}</td><td>{row.memcpy_calls} / {formatBytes(row.memcpy_bytes)}</td><td>{row.alloc_calls} / {formatBytes(row.alloc_bytes)}</td><td>{formatBytes(row.free_bytes)}</td><td>{row.sync_calls} / {row.sync_us_sum} / {row.sync_us_max} µs</td><td>{row.ioctl_calls}</td><td>{row.uvm_faults} / {row.uvm_evicts}</td><td>{row.errors}</td></tr>)}</tbody></table></div> : <p className="empty-inline">No stored aggregate rows for this process in today’s database.</p>}
  </div>;
}

export default function Processes({ store, gpus, selectedGpu, disconnected }) {
  const live = useStoreValue(store, 'procs', (state) => state.procs, 350);
  const liveAggs = useStoreValue(store, 'aggs', (state) => state.aggs, 500);
  const processApi = useApiData('/api/processes', { refreshIntervalMs: 5000 });
  const aggregateApi = useApiData('/api/aggregates?limit=100', { refreshIntervalMs: 15_000 });
  const [sort, setSort] = useState({ key: 'pid', direction: 1 });
  const [expanded, setExpanded] = useState(null);

  const rows = useMemo(() => {
    const database = processApi.data || [];
    const byKey = new Map(database.map((row) => [`${row.pid}:${row.gpu_uuid}`, row]));
    const merged = live.map((row) => {
      const meta = byKey.get(`${row.PID}:${row.GPUUUID}`);
      return { key: `${row.PID}:${row.GPUUUID}`, pid: row.PID, uuid: row.GPUUUID, command: meta?.command || `pid ${row.PID}`, gpu: meta?.gpu || gpus.find((gpu) => gpu.uuid === row.GPUUUID)?.name || `GPU ${row.GPUUUID?.slice(0, 8) || 'n/a'}`, current: row.VRAMValid ? row.VRAMBytes : null, peak: meta?.peak_vram_bytes ?? null, first: meta?.first_seen || 'n/a', running: true, fromLive: true };
    });
    const liveKeys = new Set(merged.map((row) => row.key));
    for (const row of database) {
      const key = `${row.pid}:${row.gpu_uuid}`;
      if (!liveKeys.has(key)) merged.push({ key, pid: row.pid, uuid: row.gpu_uuid, command: row.command || `pid ${row.pid}`, gpu: row.gpu || 'GPU n/a', current: row.last_vram_bytes, peak: row.peak_vram_bytes, first: row.first_seen, running: row.running, fromLive: false });
    }
    const filtered = selectedGpu ? merged.filter((row) => row.uuid === selectedGpu) : merged;
    return [...filtered].sort((a, b) => {
      const left = a[sort.key]; const right = b[sort.key];
      const order = typeof left === 'number' && typeof right === 'number' ? left - right : String(left ?? '').localeCompare(String(right ?? ''));
      return order * sort.direction;
    });
  }, [live, processApi.data, gpus, selectedGpu, sort]);

  const setSortBy = (key) => setSort((current) => ({ key, direction: current.key === key ? -current.direction : 1 }));
  const sortHead = (label, key) => <button className="sort-button" onClick={() => setSortBy(key)}>{label}{sort.key === key ? (sort.direction > 0 ? ' ↑' : ' ↓') : ''}</button>;

  return <div className="page-stack">
    <div className="page-heading"><div><p className="eyebrow">PROCESS ACTIVITY</p><h1>Processes</h1><p>Live process VRAM joined with command names and peaks from the database.</p></div></div>
    {disconnected && <div className="info-strip info-strip-hot"><strong>Daemon disconnected</strong><span>Rows may reflect the last stored process state and are greyed out.</span></div>}
    {processApi.error && <div className="inline-error">Process metadata unavailable: {processApi.error}</div>}
    {processApi.loading && live.length === 0 && <div className="panel loading-state">Loading process data…</div>}
    {!processApi.loading && !processApi.data && live.length === 0 && <div className="inline-error">{processApi.error || 'No process data is available.'}</div>}
    <section className={`panel table-panel ${disconnected ? 'stale-panel' : ''}`}>
      <div className="section-title"><div><p className="eyebrow">GPU USERS</p><h2>Running processes</h2><span className="source-note">{rows.length} rows</span></div><div className="snapshot-stack"><SnapshotControls result={processApi} label="Process metadata" /><SnapshotControls result={aggregateApi} label="Aggregates snapshot" /></div></div>
      <div className="table-wrap"><table className="process-table"><thead><tr><th>{sortHead('PID', 'pid')}</th><th>{sortHead('Command', 'command')}</th><th>GPU</th><th>{sortHead('VRAM now', 'current')}</th><th>{sortHead('VRAM peak', 'peak')}</th><th>{sortHead('First seen', 'first')}</th><th>Status</th><th>CUDA activity</th></tr></thead><tbody>
        {rows.length ? rows.map((row) => <React.Fragment key={row.key}><tr><td className="mono">{row.pid}</td><td>{row.command}</td><td>{row.gpu}</td><td>{row.current == null ? 'n/a' : formatBytes(row.current)}{!row.fromLive && <small className="cell-source">DB snapshot</small>}</td><td>{row.peak == null ? 'n/a' : formatBytes(row.peak)}</td><td>{row.first}</td><td><span className={`state-tag ${row.running ? 'state-good' : 'state-bad'}`}>{row.running ? 'Running' : 'Ended'}</span></td><td><button className="text-button" onClick={() => setExpanded(expanded === row.key ? null : row.key)}>{expanded === row.key ? 'Hide' : 'Expand'}</button></td></tr>
          {expanded === row.key && <tr className="expanded-row"><td colSpan={8}><LiveAggDetails pid={row.pid} liveRows={liveAggs} historyRows={aggregateApi.data || []} /></td></tr>}</React.Fragment>) : <tr><td colSpan={8} className="table-empty">{processApi.loading ? 'Loading…' : 'No GPU processes have arrived yet.'}</td></tr>}
      </tbody></table></div>
      {aggregateApi.error && <p className="inline-error">Stored CUDA aggregates unavailable: {aggregateApi.error}</p>}
    </section>
  </div>;
}
