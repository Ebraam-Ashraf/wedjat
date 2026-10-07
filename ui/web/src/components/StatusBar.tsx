import { Link, useLocation } from 'react-router-dom';
import Freshness, { useFreshness } from './Freshness';
import { useStoreValue } from '../hooks/useStoreValue';
import type { TelemetryStore } from '../store/telemetryStore';
import type { StatusResponse, GpuInfo } from '../types';

interface StatusBarProps {
  store: TelemetryStore;
  connected: boolean;
  status: StatusResponse | null;
  statusError: string;
  gpus: GpuInfo[];
  selectedGpu: string;
  onSelectGpu: (uuid: string) => void;
  live?: boolean;
  onToggleLive?: () => void;
  theme: string;
  onToggleTheme: () => void;
}

export default function StatusBar({
  store,
  connected,
  status,
  statusError,
  gpus,
  selectedGpu,
  onSelectGpu,
  live: _live = true,
  onToggleLive,
  theme,
  onToggleTheme,
}: StatusBarProps) {
  const location = useLocation();

  const gpu = useStoreValue(
    store,
    'gpus',
    (state) => ({
      lastGpuAt:  state.lastGpuAt,
      sequence:   state.gpuMessageSequence,
      intervalMs: state.intervalMs,
    }),
    500,
  );

  const heartbeat      = status?.heartbeat_age;
  const heartbeatStale = Number.isFinite(heartbeat) && (heartbeat ?? 0) > 30;
  const gpuFreshness   = useFreshness(gpu.lastGpuAt, gpu.intervalMs);
  const socketOnline   = connected && status?.connected !== false;

  const warning =
    statusError ||
    status?.warning ||
    (gpuFreshness.stale && gpu.lastGpuAt
      ? `Latest GPU sample is ${Math.floor(gpuFreshness.ageMs / 1000)} seconds old. The page is showing the last received readings.`
      : !gpu.lastGpuAt && connected
        ? 'Connected to the socket; waiting for the first GPU sample.'
        : heartbeatStale
          ? `Daemon heartbeat is ${heartbeat}s old.`
          : '');

  const bannerTitle = !socketOnline
    ? 'Live data unavailable'
    : gpuFreshness.stale
      ? 'GPU telemetry delayed'
      : heartbeatStale
        ? 'Daemon heartbeat stale'
        : 'Daemon warning';

  const showBanner = Boolean(warning || !socketOnline || heartbeatStale);
  const bannerClass = socketOnline && !heartbeatStale ? 'status-warning' : 'status-down';

  const liveLabel  = socketOnline ? 'Live' : 'Offline';
  const liveActive = socketOnline;

  const heartbeatLabel =
    socketOnline && Number.isFinite(heartbeat) && (heartbeat ?? -1) >= 0
      ? `${heartbeat}s heartbeat`
      : socketOnline
        ? 'heartbeat n/a'
        : 'disconnected';

  return (
    <>
      <header className="topbar">
        {/* Brand */}
        <Link className="brand" to="/">
          <span className="brand-glyph" aria-hidden="true">𓂀</span>
          <span className="brand-copy">
            <strong>WEDJAT</strong>
            <small>GPU DAEMON</small>
          </span>
        </Link>

        {/* Tools row */}
        <div className="topbar-tools">
          {/* GPU filter (only on non-root pages with multiple GPUs) */}
          {gpus.length > 1 && location.pathname !== '/' && (
            <label className="gpu-filter">
              GPU
              <select
                value={selectedGpu}
                onChange={(e) => onSelectGpu(e.target.value)}
              >
                <option value="all">All GPUs</option>
                {gpus.map((g) => (
                  <option key={g.uuid} value={g.uuid}>
                    GPU {g.index}: {g.name || g.uuid}
                  </option>
                ))}
              </select>
            </label>
          )}

          {/* Live / pause toggle */}
          {onToggleLive && (
            <button
              className={`live-toggle${liveActive ? ' is-live' : ''}`}
              type="button"
              onClick={onToggleLive}
              aria-pressed={liveActive}
              disabled={!socketOnline}
            >
              <i />
              {liveLabel}
            </button>
          )}

          {/* Theme toggle */}
          <button
            className="theme-toggle"
            type="button"
            onClick={onToggleTheme}
            title={`Switch to ${theme === 'light-green' ? 'desert' : 'light-green'} theme`}
            aria-label="Toggle theme"
          >
            {theme === 'desert' ? '☀' : '☽'}
          </button>

          {/* Heartbeat label */}
          <span className="heartbeat">{heartbeatLabel}</span>

          {/* GPU freshness dot */}
          <Freshness
            lastSampleAt={gpu.lastGpuAt}
            intervalMs={gpu.intervalMs}
            sequence={gpu.sequence}
            compact
          />

          {/* Connection pill */}
          <span className={`connection-pill${socketOnline ? ' is-online' : ' is-offline'}`}>
            <i />
            {socketOnline ? 'Connected' : 'Disconnected'}
          </span>
        </div>
      </header>

      {/* Warning banner */}
      {showBanner && (
        <div className={`status-banner ${bannerClass}`}>
          <strong>{bannerTitle}</strong>
          <span>
            {warning || 'Waiting for a daemon connection. Existing readings are stale.'}
          </span>
        </div>
      )}
    </>
  );
}
