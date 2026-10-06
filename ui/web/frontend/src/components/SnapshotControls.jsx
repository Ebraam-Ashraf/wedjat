import React from 'react';

export function formatAsOf(value) {
  if (!value) return 'not loaded';
  return new Date(value).toLocaleTimeString([], { hour12: false });
}

export default function SnapshotControls({ result, label = 'Database snapshot' }) {
  return <div className="snapshot-controls" aria-live="polite">
    <span>{label} · as of {formatAsOf(result.updatedAt)}</span>
    <button className="button-link snapshot-refresh" onClick={result.refresh} disabled={result.loading}>
      {result.loading ? 'Refreshing…' : 'Refresh'}
    </button>
  </div>;
}
