import { memo } from 'react';
import { useStoreValue } from '../hooks/useStoreValue';
import { formatBytes } from '../api';
import type { TelemetryStoreLike, LiveProcess } from '../types';

const HEAD: React.CSSProperties = { textAlign: 'right', padding: '4px 8px', color: 'var(--text-dim)', fontWeight: 500 };

function AggTable({ store }: { store: TelemetryStoreLike }) {
  const aggs = useStoreValue(store, 'aggs', (s) => s.aggs, 0);

  return (
    <div className="glass-panel" style={{ padding: 24 }}>
      <h3 style={{ margin: '0 0 16px' }}>eBPF Aggregates</h3>
      <div style={{ maxHeight: 280, overflowY: 'auto' }}>
        <table style={{ width: '100%', borderCollapse: 'collapse' }}>
          <thead>
            <tr>
              <th style={{ textAlign: 'left', padding: '4px 8px', color: 'var(--text-dim)', fontWeight: 500 }}>TGID</th>
              <th style={{ textAlign: 'left', padding: '4px 8px', color: 'var(--text-dim)', fontWeight: 500 }}>API</th>
              <th style={HEAD}>Count</th>
              <th style={HEAD}>Bytes</th>
              <th style={HEAD}>LatencySumNs</th>
              <th style={HEAD}>LatencyMaxNs</th>
              <th style={HEAD}>AllocBytes</th>
              <th style={HEAD}>FreeBytes</th>
              <th style={HEAD}>Errors</th>
              <th style={HEAD}>UvmFaults</th>
              <th style={HEAD}>UvmEvicts</th>
            </tr>
          </thead>
          <tbody>
            {(aggs as LiveProcess[]).length > 0
              ? (aggs as LiveProcess[]).map((a) => (
                  <tr key={`${a.Tgid}:${a.ApiID}`}>
                    <td style={{ padding: '4px 8px', fontFamily: 'monospace' }}>{a.Tgid}</td>
                    <td style={{ padding: '4px 8px' }}>{a.ApiID}</td>
                    <td style={HEAD}>{a.Count}</td>
                    <td style={HEAD}>{formatBytes(a.Bytes || 0)}</td>
                    <td style={HEAD}>{a.LatencySumNs}</td>
                    <td style={HEAD}>{a.LatencyMaxNs}</td>
                    <td style={HEAD}>{formatBytes(a.AllocBytes || 0)}</td>
                    <td style={HEAD}>{formatBytes(a.FreeBytes || 0)}</td>
                    <td style={HEAD}>{a.Errors}</td>
                    <td style={HEAD}>{a.UvmFaults}</td>
                    <td style={HEAD}>{a.UvmEvicts}</td>
                  </tr>
                ))
              : (
                <tr>
                  <td colSpan={11} style={{ padding: '16px 8px', textAlign: 'center', color: 'var(--text-dim)' }}>
                    No aggregates.
                  </td>
                </tr>
              )}
          </tbody>
        </table>
      </div>
    </div>
  );
}

export default memo(AggTable);
