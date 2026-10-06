import React, { memo } from 'react';
import { useStoreValue } from '../hooks/useStoreValue.js';
import { formatBytes } from '../api.js';

// eBPF aggregates table.
//
// The daemon drains eBPF at 1Hz, so this is the slowest-changing section on the
// page. Rows are keyed by tgid+apiID, which identifies an aggregate row, rather
// than by array index.

function AggTable({ store }) {
  // minIntervalMs=0: same reasoning as ProcTable — the store signature gate is
  // the real throttle; no extra floor needed here.
  const aggs = useStoreValue(store, 'aggs', (s) => s.aggs, 0);

  return (
    <div className="glass-panel" style={{ padding: 24 }}>
      <h3 style={{ margin: '0 0 16px' }}>eBPF Aggregates</h3>
      <div style={{ maxHeight: 280, overflowY: 'auto' }}>
        <table style={{ width: '100%', borderCollapse: 'collapse' }}>
          <thead><tr>
            <th style={{ textAlign: 'left', padding: '4px 8px', color: 'var(--text-muted)', fontWeight: 500 }}>TGID</th>
            <th style={{ textAlign: 'left', padding: '4px 8px', color: 'var(--text-muted)', fontWeight: 500 }}>API</th>
            <th style={{ textAlign: 'right', padding: '4px 8px', color: 'var(--text-muted)', fontWeight: 500 }}>Count</th>
            <th style={{ textAlign: 'right', padding: '4px 8px', color: 'var(--text-muted)', fontWeight: 500 }}>Bytes</th>
            <th style={{ textAlign: 'right', padding: '4px 8px', color: 'var(--text-muted)', fontWeight: 500 }}>LatencySumNs</th>
            <th style={{ textAlign: 'right', padding: '4px 8px', color: 'var(--text-muted)', fontWeight: 500 }}>LatencyMaxNs</th>
            <th style={{ textAlign: 'right', padding: '4px 8px', color: 'var(--text-muted)', fontWeight: 500 }}>AllocBytes</th>
            <th style={{ textAlign: 'right', padding: '4px 8px', color: 'var(--text-muted)', fontWeight: 500 }}>FreeBytes</th>
            <th style={{ textAlign: 'right', padding: '4px 8px', color: 'var(--text-muted)', fontWeight: 500 }}>Errors</th>
            <th style={{ textAlign: 'right', padding: '4px 8px', color: 'var(--text-muted)', fontWeight: 500 }}>UvmFaults</th>
            <th style={{ textAlign: 'right', padding: '4px 8px', color: 'var(--text-muted)', fontWeight: 500 }}>UvmEvicts</th>
          </tr></thead>
          <tbody>
            {aggs.length > 0 ? aggs.map((a) => (
              <tr key={`${a.Tgid}:${a.ApiID}`}>
                <td style={{ padding: '4px 8px', fontFamily: 'monospace' }}>{a.Tgid}</td>
                <td style={{ padding: '4px 8x' }}>{a.ApiID}</td>
                <td style={{ padding: '4px 8px', textAlign: 'right' }}>{a.Count}</td>
                <td style={{ padding: '4px 8px', textAlign: 'right' }}>{formatBytes(a.Bytes || 0)}</td>
                <td style={{ padding: '4px 8px', textAlign: 'right' }}>{a.LatencySumNs}</td>
                <td style={{ padding: '4px 8px', textAlign: 'right' }}>{a.LatencyMaxNs}</td>
                <td style={{ padding: '4px 8px', textAlign: 'right' }}>{formatBytes(a.AllocBytes || 0)}</td>
                <td style={{ padding: '4px 8px', textAlign: 'right' }}>{formatBytes(a.FreeBytes || 0)}</td>
                <td style={{ padding: '4px 8px', textAlign: 'right' }}>{a.Errors}</td>
                <td style={{ padding: '4px 8px', textAlign: 'right' }}>{a.UvmFaults}</td>
                <td style={{ padding: '4px 8px', textAlign: 'right' }}>{a.UvmEvicts}</td>
              </tr>
            )) : (
              <tr><td colSpan={11} style={{ padding: '16px 8px', textAlign: 'center', color: 'var(--text-muted)' }}>No aggregates.</td></tr>
            )}
          </tbody>
        </table>
      </div>
    </div>
  );
}

export default memo(AggTable);