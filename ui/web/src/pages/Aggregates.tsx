import { memo, useEffect } from 'react';
import AggTable from '../components/AggTable';
import type { TelemetryStoreLike } from '../types';

interface AggregatesProps {
  store: TelemetryStoreLike;
}

function Aggregates({ store }: AggregatesProps) {
  useEffect(() => {
    document.title = 'Wedjat · Aggregates';
  }, []);

  return (
    <div className="page-stack">
      <h1>Aggregates</h1>
      <AggTable store={store} />
    </div>
  );
}

export default memo(Aggregates);
