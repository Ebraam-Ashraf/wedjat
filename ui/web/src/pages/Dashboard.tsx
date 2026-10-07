import { useEffect, useMemo, useState, type ReactNode } from 'react';
import { Link } from 'react-router-dom';
import { useStoreValue } from '../hooks/useStoreValue';
import { useApiData } from '../hooks/useApiData';
import { gpuMetaFor, throttleNames } from '../gpuData';
import RealtimeChart from '../components/RealtimeChart';
import Freshness, { useFreshness } from '../components/Freshness';
import AsciiLoader from '../components/AsciiLoader';
import PyramidAnimation from '../components/PyramidAnimation';
import AsciiBox, { AsciiRule } from '../components/ui/AsciiBox';
import type { TelemetryStore } from '../store/telemetryStore';
import type { GpuInfo, GpuSample, LiveProcess, ProcessRow, Incident, AggregateRow } from '../types';

/* ---------- helpers ------------------------------------------------- */

const number = (value: unknown) =>
  Number.isFinite(Number(value)) ? Number(value) : 0;

const bytesGB = (value: unknown) =>
  value == null ? 'n/a' : `${(number(value) / 1_073_741_824).toFixed(1)}g`;

/* ---------- sub-components ------------------------------------------ */

function Metric({
  label,
  value,
  tone = '',
}: {
  label: string;
  value?: ReactNode;
  tone?: string;
}) {
  return (
    <div className="min-w-0 p-3 rounded-7xl bg-[color-mix(in_srgb,var(--chart-line)_5%,transparent)] border border-border">
      <span className="block mb-1 text-text-dim text-xs uppercase tracking-widest">
        {label}
      </span>
      <strong
        className={`block text-base font-mono tabular-nums overflow-hidden text-ellipsis whitespace-nowrap ${tone}`}
      >
        {value ?? 'n/a'}
      </strong>
    </div>
  );
}

function ProcessPanel({
  rows,
  liveRows,
  gpu,
}: {
  rows:     ProcessRow[];
  liveRows: LiveProcess[];
  gpu:      GpuInfo | null;
}) {
  const combined = useMemo(() => {
    const dbRows = rows.filter((r) => !gpu || r.gpu_uuid === gpu.uuid);
    const dbByPid = new Map(dbRows.map((r) => [Number(r.pid), r]));
    const live = liveRows.filter((r) => !gpu || r.GPUUUID === gpu.uuid);
    const livePids = new Set(live.map((r) => Number(r.PID)));

    const currentRows = live.map((r) => {
      const saved = dbByPid.get(Number(r.PID));
      return {
        ...saved,
        pid:          r.PID,
        command:      saved?.command || `PID ${r.PID}`,
        running:      true,
        currentBytes: r.VRAMValid ? r.VRAMBytes : saved?.last_vram_bytes,
      };
    });

    const savedOnly = dbRows
      .filter((r) => !livePids.has(Number(r.pid)))
      .map((r) => ({
        ...r,
        command:      r.command || `PID ${r.pid}`,
        currentBytes: r.last_vram_bytes,
      }));

    return [...currentRows, ...savedOnly].slice(0, 4);
  }, [rows, liveRows, gpu]);

  const total = number(gpu?.vram_total_bytes) || 1;

  return (
    <AsciiBox
      title="PROCESSES"
      subtitle={
        <Link
          to="/processes"
          className="text-text-dim hover:text-chart-line transition-colors text-xs font-mono"
        >
          {combined.length} traced
        </Link>
      }
      padding="tight"
      aria-label="GPU processes"
    >
      <div className="flex flex-col gap-2 py-2">
        {combined.length ? (
          combined.map((row, i) => (
            <div
              key={`${row.pid}:${row.gpu_uuid}`}
              className="glass-panel rounded-7xl p-3"
            >
              {/* Row head */}
              <div className="flex items-center gap-2 min-w-0">
                <i
                  className={`block w-[7px] h-[7px] shrink-0 rounded-full bg-chart-line shadow-glow-md ${
                    i === 1 ? 'bg-warn' : i === 2 ? 'bg-ok' : ''
                  }`}
                />
                <strong
                  className="overflow-hidden text-text text-xs font-mono font-medium text-ellipsis whitespace-nowrap flex-1"
                  title={row.command}
                >
                  {row.command}
                </strong>
                <span className="text-text-dim text-xs font-mono whitespace-nowrap">
                  pid {row.pid}
                </span>
                <b className="ml-auto text-chart-line text-xs font-mono whitespace-nowrap">
                  {bytesGB(row.currentBytes)}
                </b>
              </div>
              {/* VRAM bar */}
              <div className="util-track mt-2" style={{ height: 5 }}>
                <span
                  style={{
                    width: `${Math.min(100, Math.max(0, (number(row.currentBytes) / total) * 100))}%`,
                  }}
                />
              </div>
              {/* Row foot */}
              <div className="flex justify-between mt-2 text-text-dim text-xs">
                <span>{row.running ? 'running' : 'stored'}</span>
                <span>VRAM</span>
              </div>
            </div>
          ))
        ) : (
          <div className="panel-empty">No process metadata for this GPU yet.</div>
        )}
      </div>
      <AsciiRule />
      <Link
        className="block pt-2 pb-1 text-text-dim text-xs font-mono hover:text-chart-line transition-colors"
        to="/processes"
      >
        View process table →
      </Link>
    </AsciiBox>
  );
}

function IncidentPanel({
  incidents,
  error,
  gpu,
}: {
  incidents: Incident[] | null;
  error:     string;
  gpu:       GpuInfo | null;
}) {
  const matching = (incidents || []).filter(
    (item) => !gpu?.name || !item.gpu_name || item.gpu_name === gpu.name,
  );
  const rows = matching.slice(0, 3);

  return (
    <AsciiBox
      title="INCIDENTS"
      subtitle={
        <Link
          to="/events"
          className="text-text-dim hover:text-chart-line transition-colors text-xs font-mono"
        >
          {rows.length} recent
        </Link>
      }
      tone={rows.length > 0 ? 'warn' : 'default'}
      padding="tight"
      aria-label="Recent incidents"
    >
      <div className="flex flex-col gap-2 py-2">
        {rows.length ? (
          rows.map((item, i) => (
            <div
              key={item.incident_id || `${item.type}:${item.last_ts}`}
              className={`glass-panel rounded-7xl p-3 ${
                i === 0 ? 'border-[color-mix(in_srgb,var(--warn)_30%,transparent)]' : ''
              }`}
            >
              <div className="flex items-center gap-2 min-w-0">
                <i className="block w-[7px] h-[7px] shrink-0 rounded-full bg-warn" />
                <strong className="text-warn text-xs font-mono font-medium overflow-hidden text-ellipsis whitespace-nowrap flex-1">
                  {item.summary || item.type || 'Recorded incident'}
                </strong>
                <span className="ml-auto text-text-dim text-xs font-mono whitespace-nowrap">
                  ×{item.occurrences || 1}
                </span>
              </div>
              <p className="mt-2 mb-1 text-text-dim text-xs leading-relaxed">
                {item.detail || item.gpu_name || 'Stored daemon incident'}
              </p>
              <small className="text-text-dim text-xs">
                {item.last_time || item.first_time || 'time n/a'}
              </small>
            </div>
          ))
        ) : (
          <div className="panel-empty">
            {error ? 'Incident history is unavailable.' : 'No recorded incidents.'}
          </div>
        )}
      </div>
    </AsciiBox>
  );
}

/* ---------- main component ------------------------------------------ */

interface DashboardProps {
  store:         TelemetryStore;
  gpus:          GpuInfo[];
  selectedGpu:   string | null;
  disconnected:  boolean;
  live?:         boolean;
  onToggleLive?: () => void;
}

export default function Dashboard({
  store,
  gpus,
  selectedGpu,
  disconnected,
  live = true,
  onToggleLive: _onToggleLive,
}: DashboardProps) {
  const incoming        = useStoreValue(store, 'gpus', (s) => s.latest as GpuSample[], 0);
  const incomingProcs   = useStoreValue(store, 'procs', (s) => s.procs as LiveProcess[], 350);
  const [frozen,        setFrozen]       = useState<GpuSample[]>(incoming);
  const [frozenProcs,   setFrozenProcs]  = useState<LiveProcess[]>(incomingProcs);

  useEffect(() => {
    if (live) {
      setFrozen(incoming);
      setFrozenProcs(incomingProcs);
    }
  }, [incoming, incomingProcs, live]);

  const gpuApi       = useApiData('/api/gpus');
  const processApi   = useApiData('/api/processes', { refreshIntervalMs: 5000 });
  const incidentApi  = useApiData('/api/incidents?limit=100', { refreshIntervalMs: 15000 });
  const aggregateApi = useApiData('/api/aggregates?limit=100', { refreshIntervalMs: 15000 });

  const devices        = (gpuApi.data || gpus) as GpuInfo[];
  const samples        = (live ? incoming : frozen) as GpuSample[];
  const liveProcesses  = (live ? incomingProcs : frozenProcs) as LiveProcess[];
  const visibleSamples = selectedGpu
    ? samples.filter((s) => s.uuid === selectedGpu)
    : samples;

  const [activeUuid, setActiveUuid] = useState('');
  useEffect(() => {
    if (!activeUuid && visibleSamples.length) {
      setActiveUuid(visibleSamples[0].uuid);
    } else if (
      activeUuid &&
      !visibleSamples.some((s) => s.uuid === activeUuid) &&
      visibleSamples.length
    ) {
      setActiveUuid(visibleSamples[0].uuid);
    }
  }, [visibleSamples, activeUuid]);

  const sample      = visibleSamples.find((s) => s.uuid === activeUuid) || visibleSamples[0];
  const meta        = gpuMetaFor(sample, devices) as GpuInfo | null;
  const freshness   = useFreshness(sample?.lastSampleAt, sample?.intervalMs);
  const procRows    = (processApi.data || []) as ProcessRow[];

  const total       = number(meta?.vram_total_bytes);

  const aggregates  = ((aggregateApi.data || []) as AggregateRow[]).filter(
    (r) => !meta?.name || r.gpu_name === meta.name,
  );
  const stats = aggregates.reduce(
    (acc, r) => ({
      kernels: acc.kernels + number(r.launches),
      memcpy:  acc.memcpy  + number(r.memcpy_bytes),
      faults:  acc.faults  + number(r.uvm_faults),
      sync:    acc.sync    + number(r.sync_us_sum),
    }),
    { kernels: 0, memcpy: 0, faults: 0, sync: 0 },
  );

  const throttle = sample?.throttle == null ? [] : throttleNames(sample.throttle);

  return (
    <div className="page-stack">
      {/* GPU tab bar */}
      <div className="flex items-center justify-between min-h-8">
        <div className="flex gap-2" role="tablist" aria-label="Select GPU">
          {visibleSamples.map((gpu) => (
            <button
              key={gpu.uuid || gpu.index}
              type="button"
              role="tab"
              aria-selected={gpu.uuid === sample?.uuid}
              onClick={() => setActiveUuid(gpu.uuid)}
              className={`px-4 py-2 border rounded-pill font-mono text-xs cursor-pointer transition-colors ${
                gpu.uuid === sample?.uuid
                  ? 'text-chart-line border-chart-line bg-[color-mix(in_srgb,var(--chart-line)_10%,transparent)]'
                  : 'text-text-dim border-border bg-[rgba(255,255,255,0.045)]'
              }`}
            >
              GPU {gpu.index}
            </button>
          ))}
        </div>
      </div>

      {/* Empty / disconnected state */}
      {!sample ? (
        <section className="glass-panel empty-state rounded-xl p-8">
          <AsciiLoader label={disconnected ? 'Daemon disconnected' : 'Collecting GPU telemetry'} />
          <p className="mt-4 text-text-dim text-sm max-w-md text-center leading-relaxed">
            No GPU snapshot is sent when the socket connects. This view fills
            after the next real poll.
          </p>
        </section>
      ) : (
        <div
          className="grid gap-5"
          style={{ gridTemplateColumns: 'minmax(0, 1.4fr) minmax(300px, 1fr)' }}
        >
          {/* ── Main column ─────────────────────────────────────── */}
          <div className="flex flex-col gap-4 min-w-0">
            {/* Hero GPU Card - Terminal HUD Style */}
            <div
              className={`relative border border-[color-mix(in_srgb,var(--chart-line)_30%,transparent)] p-5 font-mono bg-panel ${
                freshness.stale ? 'opacity-80 border-warn' : ''
              }`}
            >
              {/* Corner Brackets */}
              <span className={`absolute -top-[1px] -left-[1px] w-4 h-4 border-t-2 border-l-2 pointer-events-none ${freshness.stale ? 'border-warn' : 'border-chart-line'}`} />
              <span className={`absolute -top-[1px] -right-[1px] w-4 h-4 border-t-2 border-r-2 pointer-events-none ${freshness.stale ? 'border-warn' : 'border-chart-line'}`} />
              <span className={`absolute -bottom-[1px] -left-[1px] w-4 h-4 border-b-2 border-l-2 pointer-events-none ${freshness.stale ? 'border-warn' : 'border-chart-line'}`} />
              <span className={`absolute -bottom-[1px] -right-[1px] w-4 h-4 border-b-2 border-r-2 pointer-events-none ${freshness.stale ? 'border-warn' : 'border-chart-line'}`} />

              {/* Header Row */}
              <div className="flex flex-col md:flex-row md:items-start justify-between pb-4 mb-4 border-b border-dashed border-[color-mix(in_srgb,var(--chart-line)_20%,transparent)] gap-4">
                <div className="flex flex-col gap-1">
                  <div className="flex items-center gap-3">
                    <span className="text-xl font-bold tracking-widest text-text shadow-glow-sm whitespace-nowrap">
                      [ GPU {sample.index} ]
                    </span>
                    <span className="text-chart-line text-sm uppercase tracking-wide truncate max-w-[200px] sm:max-w-xs md:max-w-md">
                      {meta?.name || 'GPU NAME UNAVAILABLE'}
                    </span>
                  </div>
                  <div className="flex flex-wrap items-center gap-4 text-xs mt-1">
                    <span className="text-text-dim uppercase tracking-widest">
                      S/N: {sample.uuid || 'UUID N/A'}
                    </span>
                    <Freshness
                      lastSampleAt={sample.lastSampleAt}
                      intervalMs={sample.intervalMs}
                      sequence={sample.sequence}
                    />
                  </div>
                </div>
                <Link
                  to={`/gpu/${encodeURIComponent(sample.uuid || sample.index)}`}
                  className="shrink-0 px-3 py-1.5 text-xs border border-chart-line text-chart-line font-bold uppercase tracking-widest hover:bg-[color-mix(in_srgb,var(--chart-line)_15%,transparent)] transition-colors"
                >
                  [ VIEW GPU {sample.index} DETAIL &rarr; ]
                </Link>
              </div>

              {/* Big utilization number */}
              <div className="flex items-end gap-3 mb-2">
                <strong
                  className="text-chart-line tabular-nums leading-none tracking-tight shadow-glow-md"
                  style={{ fontSize: 'clamp(3.5rem, 6vw, 4.5rem)' }}
                >
                  {sample.util ?? '0'}
                  {sample.util != null && (
                    <span className="text-chart-line" style={{ fontSize: 'var(--text-3xl)' }}>
                      %
                    </span>
                  )}
                </strong>
                <span className="pb-2 text-text-dim uppercase tracking-widest text-lg">util</span>
              </div>

              {/* Segmented Terminal Bar */}
              <div className="mb-6">
                <div className="flex gap-[2px] w-full h-5 border border-[color-mix(in_srgb,var(--chart-line)_30%,transparent)] p-[2px]">
                  {Array.from({ length: 40 }).map((_, i) => {
                    const activeSegments = Math.round(((sample.util ?? 0) / 100) * 40);
                    return (
                      <div
                        key={i}
                        className={`h-full flex-1 ${
                          i < activeSegments 
                            ? 'bg-chart-line shadow-glow-sm' 
                            : 'bg-chart-line/10'
                        }`}
                      />
                    );
                  })}
                </div>
                <div className="flex justify-between text-[10px] text-text-dim mt-1 px-1 tracking-widest">
                  <span>0%</span>
                  <span>25%</span>
                  <span>50%</span>
                  <span>75%</span>
                  <span>100%</span>
                </div>
              </div>

              {/* Metric Grid */}
              <div className="grid grid-cols-2 lg:grid-cols-4 gap-3 mb-4">
                {/* Temp */}
                <div className={`border p-3 flex flex-col justify-between ${
                  (sample.temp ?? 0) >= 75 ? 'border-warn' : 'border-[color-mix(in_srgb,var(--chart-line)_30%,transparent)]'
                }`}>
                  <span className="text-[10px] tracking-widest text-text-dim uppercase mb-2">
                    [ TEMP ]
                  </span>
                  <span className={`text-xl font-bold tracking-tight ${(sample.temp ?? 0) >= 75 ? 'text-warn shadow-glow-sm' : 'text-chart-line shadow-glow-sm'}`}>
                    {sample.temp == null ? 'N/A' : `${sample.temp}°C`}
                  </span>
                </div>
                
                {/* Power */}
                <div className="border border-[color-mix(in_srgb,var(--chart-line)_30%,transparent)] p-3 flex flex-col justify-between">
                  <span className="text-[10px] tracking-widest text-text-dim uppercase mb-2">
                    [ PWR ]
                  </span>
                  <span className="text-xl font-bold tracking-tight text-chart-line shadow-glow-sm">
                    {sample.power == null ? 'N/A' : `${(sample.power / 1000).toFixed(0)} W`}
                  </span>
                </div>
                
                {/* VRAM */}
                <div className="border border-[color-mix(in_srgb,var(--chart-line)_30%,transparent)] p-3 flex flex-col justify-between">
                  <span className="text-[10px] tracking-widest text-text-dim uppercase mb-2">
                    [ VRAM ]
                  </span>
                  <span className="text-lg font-bold tracking-tight text-chart-line shadow-glow-sm">
                    {sample.mem == null
                      ? 'N/A'
                      : `${bytesGB(sample.mem).replace('g', ' GB')} / ${total ? bytesGB(total).replace('g', ' GB') : 'N/A'}`}
                  </span>
                </div>
                
                {/* Clocks */}
                <div className="border border-[color-mix(in_srgb,var(--chart-line)_30%,transparent)] p-3 flex flex-col justify-between">
                  <span className="text-[10px] tracking-widest text-text-dim uppercase mb-2">
                    [ CLOCKS ]
                  </span>
                  <span className="text-base font-bold tracking-tight text-chart-line shadow-glow-sm mt-auto whitespace-nowrap">
                    {sample.clock == null ? 'N/A' : `${sample.clock} MHz`}
                    {' · '}
                    <span className="text-sm font-normal opacity-80">{sample.mem_clock == null ? 'N/A' : `${sample.mem_clock} MHz mem`}</span>
                  </span>
                </div>
              </div>

              {/* Throttle warning */}
              {throttle.length > 0 && (
                <div className="px-3 py-2 border border-warn bg-[color-mix(in_srgb,var(--warn)_10%,transparent)] text-warn font-bold tracking-widest text-sm uppercase">
                  [ ! ] THROTTLE: {throttle.join(', ')}
                </div>
              )}

              {/* Stale label */}
              {freshness.stale && (
                <div className="mt-3 text-critical text-xs uppercase tracking-widest font-mono">
                  [ ! ] LAST RECEIVED VALUES · STALE
                </div>
              )}
              
              {/* Footer text */}
              <div className="mt-4 pt-3 border-t border-[color-mix(in_srgb,var(--chart-line)_15%,transparent)] text-[10px] text-text-dim tracking-widest uppercase flex justify-between">
                 <span>WEDJAT // GPU DAEMON</span>
                 <span>DRIVER {meta?.driver_version || 'N/A'}</span>
              </div>
            </div>

            {/* CUDA activity stats row */}
            <AsciiBox
              title="CUDA ACTIVITY"
              variant="single"
              padding="tight"
              aria-label="CUDA activity statistics"
            >
              <div className="grid grid-cols-4 gap-2 py-2">
                <Metric
                  label="kernels"
                  value={aggregateApi.data ? stats.kernels.toLocaleString() : 'n/a'}
                  tone="tone-cyan"
                />
                <Metric
                  label="memcpy"
                  value={aggregateApi.data ? bytesGB(stats.memcpy) : 'n/a'}
                  tone="tone-teal"
                />
                <Metric
                  label="uvm faults"
                  value={aggregateApi.data ? stats.faults.toLocaleString() : 'n/a'}
                  tone="tone-amber"
                />
                <Metric
                  label="sync"
                  value={
                    aggregateApi.data
                      ? `${(stats.sync / 1000).toFixed(1)} ms`
                      : 'n/a'
                  }
                  tone="tone-violet"
                />
              </div>
            </AsciiBox>

            {/* History chart */}
            <AsciiBox
              title="HISTORY"
              subtitle="last 1 minute"
              variant="single"
              padding="tight"
              aria-label="Recent GPU history"
            >
              <div className="glass-panel rounded-8xl p-3 my-2">
                {live ? (
                  <RealtimeChart
                    store={store}
                    gpuIndex={sample.index}
                    field="util"
                    title="GPU utilization"
                    unit="%"
                    yDomain={[0, 100]}
                    yTicks={[0, 25, 50, 75, 100]}
                    windowMs={60000}
                    delayMs={Math.max(sample.intervalMs || 500, 500)}
                    height={138}
                  />
                ) : (
                  <div className="chart-empty">
                    Updates paused · resume to collect samples
                  </div>
                )}
              </div>
            </AsciiBox>
          </div>

          {/* ── Side column ──────────────────────────────────────── */}
          <aside className="flex flex-col gap-4 min-w-0">
            {processApi.error && (
              <div className="inline-error">
                Process metadata unavailable: {processApi.error}
              </div>
            )}

            <ProcessPanel
              rows={procRows}
              liveRows={liveProcesses}
              gpu={meta}
            />

            <IncidentPanel
              incidents={incidentApi.data as Incident[] | null}
              error={incidentApi.error}
              gpu={meta}
            />

            {aggregateApi.error && (
              <p className="muted">
                CUDA aggregate snapshot unavailable: {aggregateApi.error}
              </p>
            )}

            {/* Decorative pyramid */}
            <div
              className="flex justify-center items-end pt-4 pb-2 min-h-[300px]"
              aria-hidden="true"
            >
              <div className="opacity-60 hover:opacity-90 transition-opacity">
                <PyramidAnimation edges color axis="y" speed={0.03} w={64} h={32} />
              </div>
            </div>
          </aside>
        </div>
      )}
    </div>
  );
}
