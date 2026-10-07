import { memo, useEffect } from 'react';
import HistoryTable from '../components/HistoryTable';
import type { GpuInfo } from '../types';

interface HistoryProps {
  gpus?: GpuInfo[];
  selectedGpu?: string | null;
}

function History(_props: HistoryProps) {
  useEffect(() => {
    document.title = 'Wedjat · History';
  }, []);

  return (
    <div className="page-stack">
      <h1>History</h1>
      <div className="glass-panel rounded-xl p-6">
        <HistoryTable />
      </div>
    </div>
  );
}

export default memo(History);
