import React, { useEffect, useMemo, useState } from 'react';
import { Link } from 'react-router-dom';
import { useStoreValue } from '../hooks/useStoreValue';
import { useApiData } from '../hooks/useApiData';
import { formatBytes, gpuMetaFor, throttleNames } from '../gpuData';
import RealtimeChart from '../components/RealtimeChart';
import Freshness, { useFreshness } from '../components/Freshness';

const number = (value) => Number.isFinite(Number(value)) ? Number(value) : 0;
const bytesGB = (value) => value == null ? 'n/a' : `${(number(value) / 1073741824).toFixed(1)}g`;

function Metric({ label, value, tone = '' }) {
  return <div className="hero-metric"><span>{label}</span><strong className={tone}>{value ?? 'n/a'}</strong></div>;
}

function ProcessPanel({ rows, liveRows, gpu }) {
  const combined = useMemo(() => {
    const databaseRows = rows.filter((row) => !gpu || row.gpu_uuid === gpu.uuid);
    const databaseByPid = new Map(databaseRows.map((row) => [Number(row.pid), row]));
    const live = liveRows.filter((row) => !gpu || row.GPUUUID === gpu.uuid);
    const livePids = new Set(live.map((row) => Number(row.PID)));
    const currentRows = live.map((row) => {
      const saved = databaseByPid.get(Number(row.PID));
      return { ...saved, pid: row.PID, command: saved?.command || `PID ${row.PID}`, running: true, currentBytes: row.VRAMValid ? row.VRAMBytes : saved?.last_vram_bytes };
    });
    const savedOnly = databaseRows.filter((row) => !livePids.has(Number(row.pid))).map((row) => ({
      ...row, command: row.command || `PID ${row.pid}`, currentBytes: row.last_vram_bytes,
    }));
    return [...currentRows, ...savedOnly].slice(0, 4);
  }, [rows, liveRows, gpu]);

  return <section className="dashboard-section" aria-label="GPU processes">
    <div className="dashboard-section-head"><h2>processes</h2><Link to="/processes">{combined.length} traced</Link></div>
    <div className="dashboard-list">
      {combined.length ? combined.map((row, index) => <article className="process-glass-row" key={`${row.pid}:${row.gpu_uuid}`}>
        <div className="process-row-head"><i className={`signal-dot signal-${index % 3}`} /><strong title={row.command}>{row.command}</strong><span>pid {row.pid}</span><b>{bytesGB(row.currentBytes)}</b></div>
        <div className="process-memory"><span style={{ width: `${Math.min(100, Math.max(0, number(row.currentBytes) / (number(gpu?.vram_total_bytes) || 1) * 100))}%` }} /></div>
        <div className="process-row-foot"><span>{row.running ? 'running' : 'stored'}</span><span>VRAM</span></div>
      </article>) : <div className="panel-empty">No process metadata for this GPU yet.</div>}
    </div>
    <Link className="quiet-link" to="/processes">View process table →</Link>
  </section>;
}

function IncidentPanel({ incidents, error, gpu }) {
  const matching = (incidents || []).filter((item) => !gpu?.name || !item.gpu_name || item.gpu_name === gpu.name);
  const rows = matching.slice(0, 3);
  return <section className="dashboard-section" aria-label="Recent incidents">
    <div className="dashboard-section-head"><h2>incidents</h2><Link to="/events">{rows.length} recent</Link></div>
    <div className="dashboard-list">
      {rows.length ? rows.map((item, index) => <article className={`incident-glass-row incident-tone-${index % 2}`} key={item.incident_id || `${item.type}:${item.last_ts}`}>
        <div className="incident-row-head"><i className="signal-dot" /><strong>{item.summary || item.type || 'Recorded incident'}</strong><span>×{item.occurrences || 1}</span></div>
        <p>{item.detail || item.gpu_name || 'Stored daemon incident'}</p>
        <small>{item.last_time || item.first_time || 'time n/a'}</small>
      </article>) : <div className="panel-empty">{error ? 'Incident history is unavailable.' : 'No recorded incidents.'}</div>}
    </div>
  </section>;
}

export default function Dashboard({ store, gpus, selectedGpu, disconnected, live = true, onToggleLive }) {
  const incoming = useStoreValue(store, 'gpus', (state) => state.latest, 0);
  const incomingProcesses = useStoreValue(store, 'procs', (state) => state.procs, 350);
  const [frozen, setFrozen] = useState(incoming);
  const [frozenProcesses, setFrozenProcesses] = useState(incomingProcesses);
  useEffect(() => {
    if (live) {
      setFrozen(incoming);
      setFrozenProcesses(incomingProcesses);
    }
  }, [incoming, incomingProcesses, live]);

  const gpuApi = useApiData('/api/gpus');
  const processApi = useApiData('/api/processes', { refreshIntervalMs: 5000 });
  const incidentApi = useApiData('/api/incidents?limit=100', { refreshIntervalMs: 15000 });
  const aggregateApi = useApiData('/api/aggregates?limit=100', { refreshIntervalMs: 15000 });
  const devices = gpuApi.data || gpus;
  const samples = live ? incoming : frozen;
  const liveProcesses = live ? incomingProcesses : frozenProcesses;
  const visibleSamples = selectedGpu ? samples.filter((sample) => sample.uuid === selectedGpu) : samples;
  const [activeUuid, setActiveUuid] = useState('');
  useEffect(() => {
    if (!activeUuid && visibleSamples.length) setActiveUuid(visibleSamples[0].uuid);
    else if (activeUuid && !visibleSamples.some((sample) => sample.uuid === activeUuid) && visibleSamples.length) setActiveUuid(visibleSamples[0].uuid);
  }, [visibleSamples, activeUuid]);
  const sample = visibleSamples.find((row) => row.uuid === activeUuid) || visibleSamples[0];
  const meta = gpuMetaFor(sample, devices);
  const freshness = useFreshness(sample?.lastSampleAt, sample?.intervalMs);
  const procRows = processApi.data || [];
  const total = number(meta?.vram_total_bytes);
  const used = number(sample?.mem);
  const utilization = Math.max(0, Math.min(100, number(sample?.util)));
  const aggregates = (aggregateApi.data || []).filter((row) => !meta?.name || row.gpu_name === meta.name);
  const stats = aggregates.reduce((sum, row) => ({
    kernels: sum.kernels + number(row.launches), memcpy: sum.memcpy + number(row.memcpy_bytes),
    faults: sum.faults + number(row.uvm_faults), sync: sum.sync + number(row.sync_us_sum),
  }), { kernels: 0, memcpy: 0, faults: 0, sync: 0 });
  const throttle = sample?.throttle == null ? [] : throttleNames(sample.throttle);

  return <div className="page-stack dashboard-page">
    <div className="dashboard-toolbar">
      <div className="gpu-tabs" role="tablist" aria-label="Select GPU">
        {visibleSamples.map((gpu) => <button key={gpu.uuid || gpu.index} type="button" role="tab" aria-selected={gpu.uuid === sample?.uuid} onClick={() => setActiveUuid(gpu.uuid)}>
          GPU {gpu.index}
        </button>)}
      </div>
    </div>

    {!sample ? <section className="panel empty-state"><div className="empty-icon">◎</div><h2>{disconnected ? 'Daemon disconnected' : 'Collecting GPU telemetry…'}</h2><p>No GPU snapshot is sent when the socket connects. This view fills after the next real poll.</p></section> : <div className="dashboard-grid">
      <div className="dashboard-main-column">
        <Link className={`hero-gpu-card frost ${freshness.stale ? 'stale-panel' : ''}`} to={`/gpu/${encodeURIComponent(sample.uuid || sample.index)}`}>
          <div className="hero-card-head">
            <div><p className="gpu-kicker">GPU {sample.index} · {meta?.name || 'GPU name unavailable'}</p><span className="hero-identity">{sample.uuid || 'UUID n/a'}</span></div>
            <div className="hero-card-meta"><span>driver {meta?.driver_version || 'n/a'}</span><Freshness lastSampleAt={sample.lastSampleAt} intervalMs={sample.intervalMs} sequence={sample.sequence} /></div>
          </div>
          <div className="hero-util-row"><strong>{sample.util == null ? 'n/a' : sample.util}<small>{sample.util == null ? '' : '%'}</small></strong><span>util</span></div>
          <div className="util-track hero-util-track"><span style={{ width: `${utilization}%` }} /></div>
          <div className="hero-metrics">
            <Metric label="temp" value={sample.temp == null ? 'n/a' : `${sample.temp}°`} tone={(sample.temp ?? 0) >= 75 ? 'tone-amber' : ''} />
            <Metric label="power" value={sample.power == null ? 'n/a' : `${(sample.power / 1000).toFixed(0)} W`} />
            <Metric label="vram" value={sample.mem == null ? 'n/a' : `${bytesGB(sample.mem)} / ${total ? bytesGB(total) : 'n/a'}`} tone="tone-violet" />
          </div>
          <div className="hero-clocks"><span>clocks</span><strong>{sample.clock == null ? 'n/a' : `${sample.clock} MHz`} · {sample.mem_clock == null ? 'n/a' : `${sample.mem_clock} MHz mem`}</strong></div>
          {throttle.length > 0 && <div className="hero-warning">Throttle · {throttle.join(', ')}</div>}
          {freshness.stale && <div className="stale-label">Last received values · stale</div>}
        </Link>

        <section className="dashboard-stats" aria-label="CUDA activity">
          <Metric label="kernels" value={aggregateApi.data ? stats.kernels.toLocaleString() : 'n/a'} tone="tone-cyan" />
          <Metric label="memcpy" value={aggregateApi.data ? bytesGB(stats.memcpy) : 'n/a'} tone="tone-teal" />
          <Metric label="uvm faults" value={aggregateApi.data ? stats.faults.toLocaleString() : 'n/a'} tone="tone-amber" />
          <Metric label="sync" value={aggregateApi.data ? `${(stats.sync / 1000).toFixed(1)} ms` : 'n/a'} tone="tone-violet" />
        </section>

        <section className="dashboard-section history-section" aria-label="Recent GPU history">
          <div className="dashboard-section-head"><h2>history</h2><span>last 1 minute</span></div>
          <div className="frost history-glass">{live ? <RealtimeChart store={store} gpuIndex={sample.index} field="util" title="GPU utilization" unit="%" yDomain={[0, 100]} yTicks={[0, 25, 50, 75, 100]} windowMs={60000} delayMs={Math.max(sample.intervalMs || 500, 500)} height={138} /> : <div className="chart-empty">Updates paused · resume to collect samples</div>}</div>
        </section>
      </div>

      <aside className="dashboard-side-column">
        {processApi.error && <div className="inline-error">Process metadata unavailable: {processApi.error}</div>}
        <ProcessPanel rows={procRows} liveRows={liveProcesses} gpu={meta} />
        <IncidentPanel incidents={incidentApi.data} error={incidentApi.error} gpu={meta} />
        {aggregateApi.error && <p className="muted">CUDA aggregate snapshot unavailable: {aggregateApi.error}</p>}
      </aside>
    </div>}
  </div>;
}
