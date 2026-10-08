import { useEffect } from 'react';
import { Link, useParams } from 'react-router-dom';
import { useStoreValue } from '../hooks/useStoreValue';
import { useApiData } from '../hooks/useApiData';
import { formatBytes, throttleNames } from '../gpuData';
import Freshness, { useFreshness } from '../components/Freshness';
import RealtimeChart from '../components/RealtimeChart';
import { SmEstimate, ArchitectureIllustration } from '../components/SmEstimate';
import AsciiLoader from '../components/AsciiLoader';
import AsciiArt from '../components/AsciiArt';
import maskArt from '../../../../assets/ascii/mask.txt?raw';
import type { TelemetryStore } from '../store/telemetryStore';
import type { GpuSample, GpuInfo, LiveProcess } from '../types';

interface GpuDetailProps {
  store: TelemetryStore;
  gpus: GpuInfo[];
}

function DetailChart({
  store,
  sample,
  field,
  title,
  unit,
  yDomain,
  yTicks,
}: {
  store: TelemetryStore;
  sample: GpuSample;
  field: string;
  title: string;
  unit: string;
  yDomain: [number, number];
  yTicks: number[];
}) {
  return (
    <section className="glass-panel rounded-xl p-4">
      <RealtimeChart
        store={store}
        gpuIndex={sample.index}
        field={field}
        title={title}
        unit={unit}
        yDomain={yDomain}
        yTicks={yTicks}
        yFormat={(v) => String(Math.round(v))}
        windowMs={60000}
        delayMs={Math.max(sample.intervalMs || 500, 500)}
        height={180}
      />
    </section>
  );
}

export default function GpuDetail({ store, gpus }: GpuDetailProps) {
  const { id } = useParams<{ id: string }>();
  const gpuApi = useApiData<GpuInfo[]>('/api/gpus');
  const deviceRows = (gpuApi.data || gpus) as GpuInfo[];

  const latest = useStoreValue(store, 'gpus', (s) => s.latest as GpuSample[], 0);
  const procs = useStoreValue(store, 'procs', (s) => s.procs as LiveProcess[], 350);

  const meta = deviceRows.find((gpu) => gpu.uuid === id || String(gpu.index) === id) || null;
  const sample =
    latest.find(
      (gpu) => (meta && gpu.uuid === meta.uuid) || String(gpu.index) === id,
    ) || null;

  const freshness = useFreshness(sample?.lastSampleAt, sample?.intervalMs);
  const gpuStale = freshness.stale;

  const record = sample ? store.gpu(sample.index) : null;
  const throttle = sample?.throttle == null ? null : throttleNames(sample.throttle);
  const gpuProcs = sample ? procs.filter((p) => p.GPUUUID === sample.uuid) : [];

  useEffect(() => {
    document.title = sample ? `Wedjat · GPU ${sample.index}` : 'Wedjat · GPU detail';
  }, [sample]);

  if (!meta && !sample)
    return (
      <section className="glass-panel empty-state rounded-xl p-6">
        <AsciiArt art={maskArt} />
        <h1>GPU unavailable</h1>
        <p className="text-text-dim text-sm max-w-md text-center leading-relaxed">
          This GPU is not in the registered device list and has not sent a live sample.
        </p>
        <Link
          className="inline-flex items-center px-4 py-2 border border-border rounded-pill font-mono text-xs text-chart-line hover:bg-[color-mix(in_srgb,var(--chart-line)_10%,transparent)] transition-colors"
          to="/"
        >
          Back to Live
        </Link>
      </section>
    );

  const ring = record?.ring || [];

  const roundMax = (max: number, step: number): number => Math.ceil(max / step) * step;
  const safeMax = (values: (number | null | undefined)[], fallback: number): number => {
    const finite = values.filter((v): v is number => v != null && Number.isFinite(v));
    return finite.length > 0 ? Math.max(1, ...finite) : fallback;
  };

  const memMax = safeMax(ring.map((p) => p.mem), 8192);
  const memMaxRounded = roundMax(memMax, memMax <= 1000 ? 256 : 1024);
  const memClockMax = safeMax(ring.map((p) => p.mem_clock), 5000);
  const memClockRounded = roundMax(memClockMax, 500);
  const tempMax = safeMax(ring.map((p) => p.temp), 85);
  const tempRounded = roundMax(tempMax, 25);

  const linkCls =
    'inline-flex items-center px-4 py-2 border border-border rounded-pill font-mono text-xs text-chart-line hover:bg-[color-mix(in_srgb,var(--chart-line)_10%,transparent)] transition-colors';
  const stalePanelCls = gpuStale ? 'opacity-75' : '';

  return (
    <div className="page-stack">
      {/* Page heading */}
      <div className="page-heading">
        <div>
          <p className="eyebrow">GPU DETAIL</p>
          <h1>{meta?.name || `GPU ${sample?.index ?? id}`}</h1>
        </div>
        <Link className={linkCls} to="/">
          ← All GPUs
        </Link>
      </div>

      {gpuApi.error && (
        <div className="inline-error">GPU metadata unavailable: {gpuApi.error}.</div>
      )}

      {gpuStale && (
        <div className="info-strip info-strip-hot">
          <strong>Stale readings</strong>
          <span>
            The last sample is older than the expected cadence. Displayed values are the last
            messages received.
          </span>
        </div>
      )}

      {!sample && (
        <section className="glass-panel empty-state rounded-xl p-6">
          <AsciiLoader label="Waiting for GPU data" />
          <p className="muted text-center mt-2">
            No snapshot is sent on connect. Charts appear after the next GPU poll.
          </p>
        </section>
      )}

      {sample && (
        <>
          {/* Detail stats */}
          <div className={`glass-panel rounded-xl p-4 ${stalePanelCls}`}>
            {/* Freshness row */}
            <div className="flex items-center justify-between mb-4 pb-3 border-b border-border">
              <span className="text-text-dim text-xs font-mono uppercase tracking-wider">
                Sample freshness
              </span>
              <Freshness
                lastSampleAt={sample.lastSampleAt}
                intervalMs={sample.intervalMs}
                sequence={sample.sequence}
              />
            </div>
            {/* Stats grid */}
            <div className="grid grid-cols-2 gap-3 sm:grid-cols-3">
              {sample.util != null && (
                <div>
                  <span className="block text-text-dim text-xs font-mono uppercase tracking-wider mb-1">
                    Utilization
                  </span>
                  <strong className="font-mono tabular-nums">{sample.util}%</strong>
                </div>
              )}
              {sample.mem != null && (
                <div>
                  <span className="block text-text-dim text-xs font-mono uppercase tracking-wider mb-1">
                    VRAM
                  </span>
                  <strong className="font-mono tabular-nums">
                    {formatBytes(sample.mem)}
                    {meta?.vram_total_bytes == null
                      ? ''
                      : ` / ${formatBytes(meta.vram_total_bytes)}`}
                  </strong>
                </div>
              )}
              {sample.temp != null && (
                <div>
                  <span className="block text-text-dim text-xs font-mono uppercase tracking-wider mb-1">
                    Temperature
                  </span>
                  <strong className="font-mono tabular-nums">{sample.temp} °C</strong>
                </div>
              )}
              {sample.power != null && (
                <div>
                  <span className="block text-text-dim text-xs font-mono uppercase tracking-wider mb-1">
                    {sample.power_limit == null ? 'Power' : 'Power / limit'}
                  </span>
                  <strong className="font-mono tabular-nums">
                    {(sample.power / 1000).toFixed(1)} W
                    {sample.power_limit == null
                      ? ''
                      : ` / ${(sample.power_limit / 1000).toFixed(1)} W`}
                  </strong>
                </div>
              )}
              {sample.ecc != null && (
                <div>
                  <span className="block text-text-dim text-xs font-mono uppercase tracking-wider mb-1">
                    ECC errors
                  </span>
                  <strong className="font-mono tabular-nums">{sample.ecc}</strong>
                </div>
              )}
            </div>
          </div>

          {/* SM activity */}
          <section className={`glass-panel rounded-xl p-4 ${stalePanelCls}`}>
            <div className="mb-3">
              <p className="eyebrow">UTILIZATION ESTIMATE</p>
              <h2 className="mt-1 mb-0 text-xl">SM activity</h2>
            </div>
            <p className="text-text-dim text-sm mb-4">Estimated from whole-GPU utilization</p>
            <SmEstimate
              name={meta?.name || ''}
              vramBytes={meta?.vram_total_bytes ?? null}
              util={sample.util ?? null}
            />
          </section>

          {/* Charts grid */}
          <div className={`grid grid-cols-1 gap-4 sm:grid-cols-2 ${stalePanelCls}`}>
            <DetailChart
              store={store}
              sample={sample}
              field="util"
              title="GPU utilization"
              unit="%"
              yDomain={[0, 100]}
              yTicks={[0, 25, 50, 75, 100]}
            />
            <DetailChart
              store={store}
              sample={sample}
              field="temp"
              title="Temperature"
              unit="°C"
              yDomain={[0, tempRounded]}
              yTicks={Array.from({ length: 5 }, (_, i) => (i * tempRounded) / 4)}
            />
            <DetailChart
              store={store}
              sample={sample}
              field="mem"
              title="VRAM used"
              unit=" MB"
              yDomain={[0, memMaxRounded]}
              yTicks={Array.from({ length: 5 }, (_, i) => (i * memMaxRounded) / 4)}
            />
            <DetailChart
              store={store}
              sample={sample}
              field="mem_clock"
              title="Memory clock"
              unit=" MHz"
              yDomain={[0, memClockRounded]}
              yTicks={Array.from({ length: 5 }, (_, i) => (i * memClockRounded) / 4)}
            />
          </div>

          {/* Throttle reasons */}
          <section className={`glass-panel rounded-xl p-4 ${stalePanelCls}`}>
            <div className="mb-3">
              <p className="eyebrow">CURRENT STATUS</p>
              <h2 className="mt-1 mb-0 text-xl">Throttle reasons</h2>
            </div>
            {throttle === null ? (
              <p className="text-text-dim text-sm">
                Throttle reasons: n/a in the latest sample.
              </p>
            ) : throttle.length ? (
              <div className="flex flex-wrap gap-2">
                {throttle.map((reason) => (
                  <span
                    key={reason}
                    className="px-3 py-1 border border-warn rounded-pill text-warn text-xs font-mono"
                  >
                    {reason}
                  </span>
                ))}
              </div>
            ) : (
              <p className="text-ok text-sm">
                GPU idle — no throttle reasons.
              </p>
            )}
          </section>

          {/* Live process snapshot */}
          <section className={`glass-panel rounded-xl p-4 ${stalePanelCls}`}>
            <div className="mb-3">
              <p className="eyebrow">LIVE PROCESS SNAPSHOT</p>
              <h2 className="mt-1 mb-0 text-xl">Processes on this GPU</h2>
            </div>
            {gpuProcs.length ? (
              <div className="table-wrap">
                <table>
                  <thead>
                    <tr>
                      <th>PID</th>
                      <th>VRAM now</th>
                      <th>VRAM valid</th>
                    </tr>
                  </thead>
                  <tbody>
                    {gpuProcs.map((proc, index) => (
                      <tr key={`${String(proc.PID)}-${index}`}>
                        <td className="font-mono">{proc.PID}</td>
                        <td>
                          {proc.VRAMValid ? formatBytes(proc.VRAMBytes ?? 0) : 'n/a'}
                        </td>
                        <td>{proc.VRAMValid ? 'Yes' : 'No'}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            ) : (
              <p className="empty-inline">No process rows received for this GPU yet.</p>
            )}
            <small className="text-text-dim text-xs">
              Command names and peak VRAM are available on the Processes page from the
              database.
            </small>
          </section>
        </>
      )}

      <ArchitectureIllustration />
      {meta && (
        <small className="text-text-dim text-xs">
          {meta.pci_bus_id || 'PCI bus ID n/a'} · Total VRAM{' '}
          {meta.vram_total_bytes == null ? 'n/a' : formatBytes(meta.vram_total_bytes)} · Driver{' '}
          {meta.driver_version || 'n/a'}
        </small>
      )}
    </div>
  );
}
