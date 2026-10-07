import { memo, useEffect } from 'react';
import Chip from '../components/Chip';
import { formatBytes } from '../api';
import type { TelemetryStoreLike, GpuSample, GpuInfo } from '../types';
import { useStoreValue } from '../hooks/useStoreValue';

interface GPUsProps {
  store: TelemetryStoreLike;
  gpus: GpuInfo[];
}

function GPUs({ store, gpus }: GPUsProps) {
  const samples = useStoreValue(store, 'gpus', (s) => s.latest as GpuSample[], 0);

  useEffect(() => {
    document.title = 'Wedjat · GPUs';
  }, []);

  if (samples.length === 0) {
    return (
      <div className="page-stack">
        <h1>GPUs</h1>
        <div className="glass-panel rounded-xl p-6">
          <p className="text-text-dim text-center">No GPUs detected.</p>
        </div>
      </div>
    );
  }

  return (
    <div className="page-stack">
      <h1>GPUs</h1>
      <div className="grid gap-6 grid-cols-[repeat(auto-fill,minmax(300px,1fr))]">
        {samples.map((g) => {
          const info = gpus.find((d) => d.uuid === g.uuid || d.index === g.index);
          return (
            <div key={g.index} className="glass-panel rounded-xl p-5">
              <h3 className="mt-0 mb-3">GPU {g.index}</h3>
              <div className="flex flex-col gap-2">
                <Chip variant="neutral">{info?.name ?? g.uuid ?? 'Unknown'}</Chip>
                <Chip variant="neutral">
                  VRAM: {info?.vram_total_bytes != null ? formatBytes(info.vram_total_bytes) : 'N/A'}
                </Chip>
                <Chip variant={g.util != null && g.util > 80 ? 'critical' : 'ok'}>
                  Util: {g.util != null ? `${g.util}%` : 'N/A'}
                </Chip>
                <Chip variant={g.power != null && g.power > 200000 ? 'warn' : 'neutral'}>
                  Power: {g.power != null ? `${(g.power / 1000).toFixed(1)}W` : 'N/A'}
                </Chip>
                {g.uuid && (
                  <Chip variant="neutral" className="font-mono">
                    {g.uuid}
                  </Chip>
                )}
              </div>
            </div>
          );
        })}
      </div>
    </div>
  );
}

export default memo(GPUs);
