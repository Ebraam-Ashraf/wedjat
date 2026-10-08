import { memo, useEffect } from 'react';
import HistoryTable from '../components/HistoryTable';
import type { GpuInfo } from '../types';

interface HistoryProps {
  gpus?: GpuInfo[];
  selectedGpu?: string | null;
}

function History({ gpus = [], selectedGpu }: HistoryProps) {
  useEffect(() => {
    document.title = 'Wedjat · History';
  }, []);

  const gpu = selectedGpu
    ? gpus.find((item) => item.uuid === selectedGpu)
    : gpus[0];
  const gpuUuid = gpu?.uuid;

  return (
    <div className="page-stack">
      <h1>History</h1>
      <div className="glass-panel rounded-xl p-6">
        <HistoryTable gpuUuid={gpuUuid} />
      </div>
    </div>
  );
}

export default memo(History);
