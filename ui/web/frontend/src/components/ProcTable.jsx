import React, { memo, useEffect, useRef, useState } from 'react';
import { useStoreValue } from '../hooks/useStoreValue';
import { apiGet, formatBytes } from '../api';

// Top Processes table.
//
// The store replaces this array only when a pid's VRAM figure actually moves, so
// the 2Hz procs message costs nothing on a quiet host. Rows are keyed by pid, so
// React reuses the existing <tr> instead of remounting the whole table.
//
// Command names are not in the socket stream — the daemon only sends PID, UUID,
// and VRAM bytes. We fetch them from /api/processes (which reads meta.db) and
// keep a pid→command map. The map is refreshed whenever the pid set changes so
// newly-started processes get their name quickly.

function ProcTable({ store }) {
  // minIntervalMs=0: commit every notification immediately. The store already
  // gates notifications on the signature having changed, so this does not mean
  // "render on every socket message" — it means "render as soon as a pid's
  // VRAM actually moves", with no artificial 500ms floor added on top.
  const procs = useStoreValue(store, 'procs', (s) => s.procs, 0);

  // pid → command name, populated from /api/processes.
  const [cmdMap, setCmdMap] = useState({});
  const prevPidSet = useRef('');

  useEffect(() => {
    // Build a stable string key for the current pid set so we only hit the API
    // when a process starts or exits, not on every VRAM update.
    const pidSet = procs.map((p) => p.PID).sort().join(',');
    if (pidSet === prevPidSet.current) return;
    prevPidSet.current = pidSet;

    apiGet('/api/processes')
      .then((rows) => {
        const map = {};
        for (const r of rows) map[r.pid] = r.command || `pid ${r.pid}`;
        setCmdMap(map);
      })
      .catch(() => {}); // best-effort; table still works without names
  }, [procs]);

  return (
    <div className="glass-panel" style={{ padding: 24 }}>
      <h3 style={{ margin: '0 0 16px' }}>Top Processes</h3>
      <div style={{ maxHeight: 280, overflowY: 'auto' }}>
        <table style={{ width: '100%', borderCollapse: 'collapse' }}>
          <thead><tr>
            <th style={{ textAlign: 'left', padding: '4px 8px', color: 'var(--text-muted)', fontWeight: 500 }}>PID</th>
            <th style={{ textAlign: 'left', padding: '4px 8px', color: 'var(--text-muted)', fontWeight: 500 }}>Command</th>
            <th style={{ textAlign: 'left', padding: '4px 8px', color: 'var(--text-muted)', fontWeight: 500 }}>GPU</th>
            <th style={{ textAlign: 'right', padding: '4px 8px', color: 'var(--text-muted)', fontWeight: 500 }}>VRAM</th>
          </tr></thead>
          <tbody>
            {procs.length > 0 ? procs.map((p) => (
              <tr key={p.PID}>
                <td style={{ padding: '4px 8px', fontFamily: 'monospace' }}>{p.PID}</td>
                <td style={{ padding: '4px 8px', fontSize: '0.88rem' }}>
                  {cmdMap[p.PID] || <span style={{ color: 'var(--text-muted)' }}>pid {p.PID}</span>}
                </td>
                <td style={{ padding: '4px 8px', fontSize: '0.85rem', color: 'var(--text-muted)' }}>{p.GPUUUID?.slice(0, 8) ?? '-'}</td>
                <td style={{ padding: '4px 8px', textAlign: 'right' }}>{formatBytes(p.VRAMBytes)}</td>
              </tr>
            )) : (
              <tr><td colSpan={4} style={{ padding: '16px 8px', textAlign: 'center', color: 'var(--text-muted)' }}>No processes.</td></tr>
            )}
          </tbody>
        </table>
      </div>
    </div>
  );
}

export default memo(ProcTable);
