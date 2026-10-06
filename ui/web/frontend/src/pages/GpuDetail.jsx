import React from 'react';
import { Link, useParams } from 'react-router-dom';
import { useStoreValue } from '../hooks/useStoreValue.js';
import { useApiData } from '../hooks/useApiData.js';
import { formatBytes, throttleNames } from '../gpuData.js';
import RealtimeChart from '../components/RealtimeChart.jsx';
import { SmEstimate } from '../components/SmEstimate.jsx';
import { ArchitectureIllustration } from '../components/SmEstimate.jsx';
import Freshness, { useFreshness } from '../components/Freshness.jsx';

function DetailChart({ store, sample, field, title, unit, yDomain, yTicks, yFormat, scale }) {
  return <div className="panel metric-chart">
    <RealtimeChart
      store={store}
      gpuIndex={sample.index}
      field={field}
      title={title}
      unit={unit}
      yDomain={yDomain}
      yTicks={yTicks}
      yFormat={yFormat}
      scale={scale}
      windowMs={60000}
      delayMs={Math.max(sample.intervalMs || 500, 500)}
      height={180}
    />
  </div>;
}

export default function GpuDetail({ store, gpus }) {
  const { id } = useParams();
  const gpuApi = useApiData('/api/gpus');
  const deviceRows = gpuApi.data || gpus;
  // No throttle on hero stats - update immediately when samples arrive
  const latest = useStoreValue(store, 'gpus', (state) => state.latest, 0);
  const procs = useStoreValue(store, 'procs', (state) => state.procs, 0);
  const meta = deviceRows.find((gpu) => gpu.uuid === id || String(gpu.index) === id) || null;
  const sample = latest.find((gpu) => (meta && gpu.uuid === meta.uuid) || String(gpu.index) === id) || null;
  const freshness = useFreshness(sample?.lastSampleAt, sample?.intervalMs);
  const gpuStale = freshness.stale;
  const record = sample ? store.gpu(sample.index) : null;
  const throttle = sample?.throttle == null ? null : throttleNames(sample.throttle);
  const gpuProcs = sample ? procs.filter((proc) => proc.GPUUUID === sample.uuid) : [];

  if (!meta && !sample) return <section className="panel empty-state"><h1>GPU unavailable</h1><p>This GPU is not in the registered device list and has not sent a live sample.</p><Link to="/">Back to Live</Link></section>;

  // Calculate y-axis ranges from actual data
  const ring = record?.ring || [];
  const memMax = Math.max(1, ...ring.flatMap((point) => [point.mem || 0]));
  const memClockMax = Math.max(1, ...ring.flatMap((point) => [point.mem_clock || 0]));
  const tempMax = Math.max(1, ...ring.flatMap((point) => [point.temp || 0]));

  // Nice round numbers for y-axis
  const roundMax = (max, step) => Math.ceil(max / step) * step;
  const memStep = memMax <= 1000 ? 256 : 1024;

  return <div className="page-stack">
    <div className="page-heading"><div><p className="eyebrow">GPU DETAIL</p><h1>{meta?.name || `GPU ${sample?.index ?? id}`}</h1><p>{meta?.uuid || sample?.uuid || 'UUID n/a'} · index {meta?.index ?? sample?.index ?? 'n/a'}</p></div><Link className="button-link" to="/">← All GPUs</Link></div>
    {gpuApi.error && <div className="inline-error">GPU metadata unavailable: {gpuApi.error}.</div>}
    {gpuStale && <div className="info-strip info-strip-hot"><strong>Stale readings</strong><span>The last sample is older than the expected cadence. Displayed values are the last messages received.</span></div>}
    {!sample && <div className="info-strip"><strong>Collecting live samples</strong><span>No snapshot is sent on connect. Charts appear after the next GPU poll.</span></div>}
    {sample && <>
      <div className={`detail-stats panel ${gpuStale ? 'stale-panel' : ''}`}>
        <div className="detail-freshness"><span>Sample freshness</span><Freshness lastSampleAt={sample.lastSampleAt} intervalMs={sample.intervalMs} sequence={sample.sequence} /></div>
        {sample.util != null && <div><span>Utilization</span><strong>{sample.util}%</strong></div>}
        {sample.mem != null && <div><span>VRAM</span><strong>{formatBytes(sample.mem)}{meta?.vram_total_bytes == null ? '' : ` / ${formatBytes(meta.vram_total_bytes)}`}</strong></div>}
        {sample.temp != null && <div><span>Temperature</span><strong>{sample.temp} °C</strong></div>}
        {sample.power != null && <div><span>{sample.power_limit == null ? 'Power' : 'Power / limit'}</span><strong>{(sample.power / 1000).toFixed(1)} W{sample.power_limit == null ? '' : ` / ${(sample.power_limit / 1000).toFixed(1)} W`}</strong></div>}
        {sample.ecc != null && <div><span>ECC error counter</span><strong>{sample.ecc}</strong></div>}
      </div>
      <section className={`panel ${gpuStale ? 'stale-panel' : ''}`}>
        <div className="section-title"><div><p className="eyebrow">UTILIZATION ESTIMATE</p><h2>SM activity</h2></div><span>Estimated from whole-GPU utilization</span></div>
        <SmEstimate name={meta?.name || ''} vramBytes={meta?.vram_total_bytes ?? null} util={sample.util} />
      </section>
      <div className={`chart-grid detail-chart-grid ${gpuStale ? 'stale-panel' : ''}`}>
        <DetailChart store={store} sample={sample} field="util" title="GPU utilization" unit="%" yDomain={[0, 100]} yTicks={[0, 25, 50, 75, 100]} />
        <DetailChart store={store} sample={sample} field="temp" title="Temperature" unit=" °C" yDomain={[0, roundMax(tempMax, 25)]} yTicks={Array.from({ length: 5 }, (_, i) => i * roundMax(tempMax, 25) / 4)} />
        <DetailChart store={store} sample={sample} field="mem" title="VRAM used" unit=" MB" yDomain={[0, roundMax(memMax, memStep)]} yTicks={Array.from({ length: 5 }, (_, i) => i * roundMax(memMax, memStep) / 4)} />
        <DetailChart store={store} sample={sample} field="mem_clock" title="Memory clock" unit=" MHz" yDomain={[0, roundMax(memClockMax, 500)]} yTicks={Array.from({ length: 5 }, (_, i) => i * roundMax(memClockMax, 500) / 4)} />
      </div>
      <section className={`panel ${gpuStale ? 'stale-panel' : ''}`}>
        <div className="section-title"><div><p className="eyebrow">CURRENT STATUS</p><h2>Throttle reasons</h2></div></div>
        {throttle === null ? <p className="muted">Throttle reasons: n/a in the latest sample.</p> : throttle.length ? <div className="badge-list">{throttle.map((reason) => <span className="throttle-badge" key={reason}>{reason}</span>)}</div> : <p className="text-good">No throttle reason reported in the latest sample.</p>}
      </section>
      <section className={`panel ${gpuStale ? 'stale-panel' : ''}`}>
        <div className="section-title"><div><p className="eyebrow">LIVE PROCESS SNAPSHOT</p><h2>Processes on this GPU</h2></div></div>
        {gpuProcs.length ? <div className="table-wrap"><table><thead><tr><th>PID</th><th>VRAM now</th><th>VRAM valid</th></tr></thead><tbody>{gpuProcs.map((proc, index) => <tr key={`${proc.PID}-${index}`}><td className="mono">{proc.PID}</td><td>{proc.VRAMValid ? formatBytes(proc.VRAMBytes) : 'n/a'}</td><td>{proc.VRAMValid ? 'Yes' : 'No'}</td></tr>)}</tbody></table></div> : <p className="empty-inline">No process rows received for this GPU yet.</p>}
        <small className="muted">Command names and peak VRAM are available on the Processes page from the database.</small>
      </section>
    </>}
    <ArchitectureIllustration />
    {meta && <small className="muted">{meta.pci_bus_id || 'PCI bus ID n/a'} · Total VRAM {meta.vram_total_bytes == null ? 'n/a' : formatBytes(meta.vram_total_bytes)} · Driver {meta.driver_version || 'n/a'}</small>}
  </div>;
}
