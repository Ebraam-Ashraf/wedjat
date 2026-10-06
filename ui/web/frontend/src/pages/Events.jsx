import React from 'react';
import { useStoreValue } from '../hooks/useStoreValue';
import { useApiData } from '../hooks/useApiData';
import { API_NAMES, throttleNames } from '../gpuData';
import SnapshotControls from '../components/SnapshotControls';

function liveEventText(event) {
  if (event.type === 'xid') return { title: `Xid ${event.data?.Code}`, details: `GPU ${event.data?.Index} · ${event.data?.UUID || 'UUID n/a'}` };
  const data = event.data || {};
  const hung = data.ApiID === 8 && ((data.Flags & (1 << 8)) !== 0);
  const stall = data.ApiID === 8 && data.LatencyNs >= 250_000_000;
  const title = hung ? 'Sync hang detected' : stall ? 'Sync stall' : API_NAMES[data.ApiID] || `eBPF event ${data.ApiID}`;
  return { title, details: `PID ${data.Tgid} · TID ${data.Tid} · CUDA ordinal ${data.DeviceOrdinal === 0xffffffff ? 'unknown' : data.DeviceOrdinal} · ${data.LatencyNs} ns · ${data.Bytes} bytes · status ${data.Status}` };
}

function shortTime(ns) {
  const value = Number(ns);
  return Number.isFinite(value) && value > 0 ? new Date(value / 1e6).toLocaleString() : 'Time n/a';
}

export default function Events({ store, gpus, selectedGpu, disconnected }) {
  const events = useStoreValue(store, 'events', (state) => state.events, 0);
  const latest = useStoreValue(store, 'gpus', (state) => state.latest, 400);
  const incidentApi = useApiData('/api/incidents?limit=100', { refreshIntervalMs: 15_000 });
  const gpuApi = useApiData('/api/gpus');
  const devices = gpuApi.data || gpus;
  const currentGpu = new Map(latest.map((gpu) => [gpu.uuid, gpu]));
  const activeThrottle = devices.flatMap((meta) => {
    if (selectedGpu && meta.uuid !== selectedGpu) return [];
    const sample = currentGpu.get(meta.uuid);
    if (sample?.throttle == null) return [];
    const reasons = throttleNames(sample.throttle);
    return reasons.length ? [{ meta, reasons }] : [];
  });
  const throttleUnavailable = currentGpu.size === 0 || devices.some((meta) => (!selectedGpu || meta.uuid === selectedGpu) && currentGpu.get(meta.uuid)?.throttle == null);
  const timeline = [
    ...events.filter((event) => !selectedGpu || event.type !== 'xid' || event.data?.UUID === selectedGpu).map((event) => ({ key: event.id, kind: 'live', time: Number(event.ts), ...liveEventText(event), source: event.type === 'xid' ? 'Live Xid' : 'Live eBPF' })),
    ...(incidentApi.data || []).filter((incident) => !selectedGpu || devices.some((gpu) => gpu.uuid === selectedGpu && gpu.name === incident.gpu_name)).map((incident) => ({ key: `incident:${incident.incident_id}`, kind: 'incident', time: Number(incident.last_ts) * 1e9, title: incident.summary || incident.type, details: incident.detail || '', source: `Stored incident · ${incident.type}`, first: incident.first_time, last: incident.last_time, occurrences: incident.occurrences })),
  ].sort((a, b) => b.time - a.time);

  return <div className="page-stack">
    <div className="page-heading"><div><p className="eyebrow">FAULTS & DIAGNOSTICS</p><h1>Events</h1><p>Live socket events alongside incidents stored by the daemon.</p></div><span className="source-note">Live buffer: 50 · stored query: latest 100</span></div>
    {disconnected && <div className="info-strip info-strip-hot"><strong>Daemon disconnected</strong><span>Live events are no longer arriving. Existing entries are stale.</span></div>}
    {gpuApi.error && <div className="inline-error">GPU metadata unavailable: {gpuApi.error}; GPU-specific incident filtering may be incomplete.</div>}
    <section className={`panel ${disconnected ? 'stale-panel' : ''}`}>
      <div className="section-title"><div><p className="eyebrow">CURRENT TELEMETRY</p><h2>Throttle state</h2></div></div>
      {activeThrottle.length ? <div className="throttle-list">{activeThrottle.map(({ meta, reasons }) => <div className="throttle-row" key={meta.uuid}><strong>GPU {meta.index} · {meta.name || meta.uuid}</strong><span>{reasons.join(' · ')}</span></div>)}</div> : throttleUnavailable ? <p className="muted">Throttle reasons: n/a in the latest sample.</p> : <p className="text-good">No throttle reasons reported in the latest GPU samples.</p>}
      <p className="muted compact-note">Throttle reasons are current sample bitmasks, not a stored event history. The daemon does not create throttle incidents. Live eBPF event ordinals cannot always be mapped to a physical GPU, so those events remain visible under any GPU filter.</p>
    </section>
    {incidentApi.error && <div className="inline-error">Stored incidents unavailable: {incidentApi.error}</div>}
    <section className={`panel ${disconnected ? 'stale-panel' : ''}`}>
      <div className="section-title"><div><p className="eyebrow">EVENT TIMELINE</p><h2>Live and recorded events</h2></div><SnapshotControls result={incidentApi} label="Incidents snapshot" /></div>
      {timeline.length ? <div className={`timeline ${disconnected ? 'stale-panel' : ''}`}>{timeline.map((entry) => <article className={`timeline-item ${entry.kind === 'incident' ? 'timeline-incident' : ''}`} key={entry.key}>
        <div className="timeline-marker" />
        <div className="timeline-body"><div className="timeline-heading"><strong>{entry.title}</strong><span className="state-tag">{entry.source}</span></div>
          <p>{entry.details}</p><small>{entry.kind === 'incident' ? `${entry.first || 'n/a'} → ${entry.last || 'n/a'} · ${entry.occurrences} occurrence(s)` : shortTime(entry.time)}</small></div>
      </article>)}</div> : <div className="empty-state small-empty"><h3>{incidentApi.loading ? 'Loading incidents…' : 'No events received'}</h3><p>New live events appear here as the socket reports them. No initial event snapshot is sent.</p></div>}
    </section>
  </div>;
}
