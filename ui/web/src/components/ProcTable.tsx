import { memo, useEffect, useRef, useState } from 'react';
import { useStoreValue } from '../hooks/useStoreValue';
import { apiGet, formatBytes } from '../api';
import type { TelemetryStoreLike, LiveProcess, ProcessRow } from '../types';

function ProcTable({ store }: { store: TelemetryStoreLike }) {
  const procs = useStoreValue(store, 'procs', (s) => s.procs, 0);

  const [cmdMap, setCmdMap] = useState<Record<string, string>>({});
  const prevPidSet = useRef('');

  useEffect(() => {
    const pidSet = (procs as LiveProcess[]).map((p) => p.PID).sort().join(',');
    if (pidSet === prevPidSet.current) return;
    prevPidSet.current = pidSet;

    apiGet<ProcessRow[]>('/api/processes')
      .then((rows) => {
        const map: Record<string, string> = {};
        for (const r of rows) map[String(r.pid)] = r.command || `pid ${r.pid}`;
        setCmdMap(map);
      })
      .catch(() => {});
  }, [procs]);

  return (
    <div className="glass-panel" style={{ padding: 24 }}>
      <h3 style={{ margin: '0 0 16px' }}>Top Processes</h3>
      <div style={{ maxHeight: 280, overflowY: 'auto' }}>
        <table style={{ width: '100%', borderCollapse: 'collapse' }}>
          <thead>
            <tr>
              <th style={{ textAlign: 'left', padding: '4px 8px', color: 'var(--text-dim)', fontWeight: 500 }}>PID</th>
              <th style={{ textAlign: 'left', padding: '4px 8px', color: 'var(--text-dim)', fontWeight: 500 }}>Command</th>
              <th style={{ textAlign: 'left', padding: '4px 8px', color: 'var(--text-dim)', fontWeight: 500 }}>GPU</th>
              <th style={{ textAlign: 'right', padding: '4px 8px', color: 'var(--text-dim)', fontWeight: 500 }}>VRAM</th>
            </tr>
          </thead>
          <tbody>
            {(procs as LiveProcess[]).length > 0
              ? (procs as LiveProcess[]).map((p) => (
                  <tr key={p.PID}>
                    <td style={{ padding: '4px 8px', fontFamily: 'monospace' }}>{p.PID}</td>
                    <td style={{ padding: '4px 8px', fontSize: '0.88rem' }}>
                      {cmdMap[p.PID as string] || <span style={{ color: 'var(--text-dim)' }}>pid {p.PID}</span>}
                    </td>
                    <td style={{ padding: '4px 8px', fontSize: '0.85rem', color: 'var(--text-dim)' }}>
                      {p.GPUUUID?.slice(0, 8) ?? '-'}
                    </td>
                    <td style={{ padding: '4px 8px', textAlign: 'right' }}>
                      {formatBytes(p.VRAMBytes ?? 0)}
                    </td>
                  </tr>
                ))
              : (
                <tr>
                  <td colSpan={4} style={{ padding: '16px 8px', textAlign: 'center', color: 'var(--text-dim)' }}>
                    No processes.
                  </td>
                </tr>
              )}
          </tbody>
        </table>
      </div>
    </div>
  );
}

export default memo(ProcTable);
