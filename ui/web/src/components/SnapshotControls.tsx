import type { ApiDataResult } from '../types';

export function formatAsOf(value: Date | null | undefined): string {
  if (!value) return 'not loaded';
  return new Date(value).toLocaleTimeString([], { hour12: false });
}

export interface SnapshotControlsProps {
  result: ApiDataResult<unknown>;
  label?: string;
}

export default function SnapshotControls({ result, label = 'Database snapshot' }: SnapshotControlsProps) {
  return (
    <div className="snapshot-controls" aria-live="polite">
      <span>{label} · as of {formatAsOf(result.updatedAt)}</span>
      <button
        className="button-link snapshot-refresh"
        onClick={() => void result.refresh()}
        disabled={result.loading}
      >
        {result.loading ? 'Refreshing…' : 'Refresh'}
      </button>
    </div>
  );
}
