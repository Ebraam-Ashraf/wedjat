import { memo, useEffect } from 'react';
import ProcTable from '../components/ProcTable';
import AggTable from '../components/AggTable';
import EventsList from '../components/EventsList';
import type { TelemetryStoreLike, GpuInfo } from '../types';

interface ProcessesProps {
  store: TelemetryStoreLike;
  gpus?: GpuInfo[];
  selectedGpu?: string | null;
  disconnected?: boolean;
}

function Processes({ store }: ProcessesProps) {
  useEffect(() => {
    document.title = 'Wedjat · Processes';
  }, []);

  return (
    <div className="page-stack">
      <h1>Processes</h1>
      <ProcTable store={store} />
      <AggTable store={store} />
      <EventsList store={store} />
    </div>
  );
}

export default memo(Processes);
