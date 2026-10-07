import { memo, useEffect, useState } from 'react';
import { apiGet } from '../api';
import type { HistoryRow } from '../types';

export interface HistoryTableProps {
  limit?: number;
}

function HistoryTable({ limit = 100 }: HistoryTableProps) {
  const [rows, setRows] = useState<HistoryRow[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    apiGet<HistoryRow[]>('/api/history')
      .then((data) => {
        if (cancelled) return;
        setRows(data.slice(0, limit));
        setLoading(false);
      })
      .catch((err: Error) => {
        if (cancelled) return;
        setError(err.message);
        setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [limit]);

  if (loading) return <p style={{ color: 'var(--text-dim)' }}>Loading history…</p>;
  if (error) return <p style={{ color: 'var(--critical)' }}>Error: {error}</p>;
  if (rows.length === 0) return <p style={{ color: 'var(--text-dim)' }}>No history available.</p>;

  return (
    <div style={{ maxHeight: 400, overflowY: 'auto' }}>
      <table style={{ width: '100%', borderCollapse: 'collapse' }}>
        <thead>
          <tr>
            <th style={{ textAlign: 'left', padding: 8, color: 'var(--text-dim)', fontWeight: 500 }}>Timestamp</th>
            <th style={{ textAlign: 'right', padding: 8, color: 'var(--text-dim)', fontWeight: 500 }}>GPU Util %</th>
            <th style={{ textAlign: 'right', padding: 8, color: 'var(--text-dim)', fontWeight: 500 }}>Mem Util %</th>
            <th style={{ textAlign: 'right', padding: 8, color: 'var(--text-dim)', fontWeight: 500 }}>Temp °C</th>
            <th style={{ textAlign: 'right', padding: 8, color: 'var(--text-dim)', fontWeight: 500 }}>Power W</th>
            <th style={{ textAlign: 'right', padding: 8, color: 'var(--text-dim)', fontWeight: 500 }}>VRAM</th>
          </tr>
        </thead>
        <tbody>
          {rows.map((r) => (
            <tr key={r.id}>
              <td style={{ padding: 6, fontFamily: 'monospace' }}>{new Date(r.ts).toLocaleString()}</td>
              <td style={{ padding: 6, textAlign: 'right' }}>{r.util_gpu?.toFixed(1) ?? 'n/a'}</td>
              <td style={{ padding: 6, textAlign: 'right' }}>{r.mem_util?.toFixed(1) ?? 'n/a'}</td>
              <td style={{ padding: 6, textAlign: 'right' }}>{r.temp?.toFixed(0) ?? 'n/a'}</td>
              <td style={{ padding: 6, textAlign: 'right' }}>
                {r.power != null ? (r.power / 1000).toFixed(1) : 'n/a'}
              </td>
              <td style={{ padding: 6, textAlign: 'right' }}>
                {r.vram_used != null
                  ? `${(r.vram_used / 1_048_576).toFixed(0)} MB`
                  : r.vram_used_max_bytes != null
                    ? `${(r.vram_used_max_bytes / 1_073_741_824).toFixed(2)} GB`
                    : 'n/a'}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

export default memo(HistoryTable);
