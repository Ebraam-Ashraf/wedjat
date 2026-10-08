import { memo, useEffect, useState } from 'react';
import { apiGet } from '../api';
import type { HistoryRow } from '../types';

export interface HistoryTableProps {
  limit?: number;
  gpuUuid?: string;
}

function HistoryTable({ limit = 100, gpuUuid }: HistoryTableProps) {
  const [rows, setRows] = useState<HistoryRow[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!gpuUuid) {
      setLoading(false);
      return;
    }
    let cancelled = false;
    setLoading(true);
    apiGet<HistoryRow[]>(`/api/history?gpu_uuid=${encodeURIComponent(gpuUuid)}`)
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
  }, [gpuUuid, limit]);

  if (!gpuUuid) return <p style={{ color: 'var(--text-dim)' }}>Waiting for GPU metadata…</p>;
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
            <tr key={`${r.ts}-${r.gpu_uuid}`}>
              <td style={{ padding: 6, fontFamily: 'monospace' }}>{new Date(r.ts * 1000).toLocaleString()}</td>
              <td style={{ padding: 6, textAlign: 'right' }}>
                {r.util_gpu_avg != null ? r.util_gpu_avg.toFixed(1) : 'n/a'}
              </td>
              <td style={{ padding: 6, textAlign: 'right' }}>
                {r.util_gpu_max != null ? r.util_gpu_max.toFixed(1) : 'n/a'}
              </td>
              <td style={{ padding: 6, textAlign: 'right' }}>
                {r.temp_max_c != null ? r.temp_max_c.toFixed(0) : 'n/a'}
              </td>
              <td style={{ padding: 6, textAlign: 'right' }}>
                {r.power_mw_sum != null ? (r.power_mw_sum / 1_000_000 / r.n).toFixed(1) : 'n/a'}
              </td>
              <td style={{ padding: 6, textAlign: 'right' }}>
                {r.vram_used_max_bytes != null
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
