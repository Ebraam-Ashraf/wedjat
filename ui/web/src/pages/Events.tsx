import { memo, useEffect } from 'react';
import EventsList from '../components/EventsList';
import type { TelemetryStoreLike, GpuInfo } from '../types';

interface EventsProps {
  store: TelemetryStoreLike;
  gpus?: GpuInfo[];
  selectedGpu?: string | null;
  disconnected?: boolean;
}

function Events({ store }: EventsProps) {
  useEffect(() => {
    document.title = 'Wedjat · Events';
  }, []);

  return (
    <div className="page-stack">
      <h1>Events</h1>
      <EventsList store={store} />
    </div>
  );
}

export default memo(Events);
