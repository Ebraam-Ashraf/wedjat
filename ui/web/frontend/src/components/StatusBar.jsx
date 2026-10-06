import React from 'react';
import { Link, useLocation } from 'react-router-dom';
import Freshness, { useFreshness } from './Freshness';
import { useStoreValue } from '../hooks/useStoreValue';

export default function StatusBar({ store, connected, status, statusError, gpus, selectedGpu, onSelectGpu, live = true, onToggleLive }) {
  const location = useLocation();
  const gpu = useStoreValue(store, 'gpus', (state) => ({
    lastGpuAt: state.lastGpuAt,
    sequence: state.gpuMessageSequence,
    intervalMs: state.intervalMs,
  }), 500);
  const heartbeat = status?.heartbeat_age;
  const heartbeatStale = Number.isFinite(heartbeat) && heartbeat > 30;
  const gpuFreshness = useFreshness(gpu.lastGpuAt, gpu.intervalMs);
  const socketOnline = connected && status?.connected !== false;
  const warning = statusError || status?.warning || (gpuFreshness.stale && gpu.lastGpuAt
    ? `Latest GPU sample is ${Math.floor(gpuFreshness.ageMs / 1000)} seconds old. The page is showing the last received readings.`
    : !gpu.lastGpuAt && connected
      ? 'Connected to the socket; waiting for the first GPU sample.'
      : heartbeatStale ? `Daemon heartbeat is ${heartbeat}s old.` : '');
  const bannerTitle = !socketOnline
    ? 'Live data unavailable'
    : gpuFreshness.stale
      ? 'GPU telemetry delayed'
      : heartbeatStale
        ? 'Daemon heartbeat stale'
        : 'Daemon warning';
  const showBanner = Boolean(warning || !socketOnline || heartbeatStale);
  const bannerClass = socketOnline && !heartbeatStale ? 'status-warning' : 'status-down';

  return (
    <>
      <header className="topbar">
        <Link className="brand" to="/"><i className="brand-mark" aria-hidden="true">◇</i><span className="brand-copy"><strong>Wedjat</strong><small>GPU DAEMON</small></span></Link>
        <div className="topbar-tools">
          {gpus.length > 1 && location.pathname !== '/' && <label className="gpu-filter">GPU
            <select value={selectedGpu} onChange={(event) => onSelectGpu(event.target.value)}>
              <option value="all">All GPUs</option>
              {gpus.map((gpu) => <option key={gpu.uuid} value={gpu.uuid}>GPU {gpu.index}: {gpu.name || gpu.uuid}</option>)}
            </select>
          </label>}
          {onToggleLive && <button className={`live-toggle ${live ? 'is-live' : ''}`} type="button" onClick={onToggleLive} aria-pressed={live}><i />{live ? 'Live' : 'Paused'}</button>}
          <span className="heartbeat">{Number.isFinite(heartbeat) && heartbeat >= 0 ? `${heartbeat}s heartbeat` : 'heartbeat n/a'}</span>
          <Freshness lastSampleAt={gpu.lastGpuAt} intervalMs={gpu.intervalMs} sequence={gpu.sequence} compact />
          <span className={`connection-pill ${socketOnline ? 'is-online' : 'is-offline'}`}><i />{socketOnline ? 'Connected' : 'Disconnected'}</span>
        </div>
      </header>
      {showBanner && <div className={`status-banner ${bannerClass}`}>
        <strong>{bannerTitle}</strong>
        <span>{warning || 'Waiting for a daemon connection. Existing readings are stale.'}</span>
      </div>}
    </>
  );
}
