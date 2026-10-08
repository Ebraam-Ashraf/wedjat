import { memo, useEffect, useState } from 'react';
import type { Incident } from '../types';
import { useApiData } from '../hooks/useApiData';

interface IncidentsProps {
  store?: unknown;
}

const INCIDENT_HELP = [
  { type: 'sync_stall', trigger: 'Sync latency ≥ 250 ms', cause: 'GPU oversubscribed, thermal throttle, heavy compute' },
  { type: 'sync_hang',  trigger: 'Driver FlagHungSync set',  cause: 'GPU hang (driver/kernel bug, HW fault)' },
  { type: 'xid',        trigger: 'NVIDIA Xid interrupt',     cause: 'ECC error, thermal, power, NVLink failure' },
];

function Incidents(_props: IncidentsProps) {
  const { data, loading, error } = useApiData<Incident[]>('/api/incidents?limit=100', { refreshIntervalMs: 15000 });
  const incidents = data || [];
  const [showHelp, setShowHelp] = useState(false);

  useEffect(() => {
    document.title = 'Wedjat · Incidents';
  }, []);

  if (loading)
    return (
      <div className="page-stack">
        <h1>Incidents</h1>
        <div className="glass-panel rounded-xl p-6">
          <p className="text-text-dim">Loading incidents…</p>
        </div>
      </div>
    );

  if (error)
    return (
      <div className="page-stack">
        <h1>Incidents</h1>
        <div className="glass-panel rounded-xl p-6">
          <p className="text-critical">Error: {error}</p>
        </div>
      </div>
    );

  return (
    <div className="page-stack">
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 16 }}>
        <h1>Incidents</h1>
        <button
          type="button"
          onClick={() => setShowHelp(!showHelp)}
          style={{
            padding: '6px 12px',
            fontSize: '0.8rem',
            background: 'var(--bg-elevated)',
            border: '1px solid var(--border)',
            borderRadius: 6,
            color: 'var(--text)',
            cursor: 'pointer',
          }}
        >
          {showHelp ? 'Hide legend' : 'Show incident types'}
        </button>
      </div>
      <div className="glass-panel rounded-xl p-6">
        {showHelp && (
          <details style={{ marginBottom: 16, padding: 12, background: 'var(--bg-elevated)', borderRadius: 8, border: '1px solid var(--border)' }}>
            <summary style={{ cursor: 'pointer', fontWeight: 600, marginBottom: 8 }}>Incident type reference</summary>
            <table style={{ width: '100%', borderCollapse: 'collapse', fontSize: '0.85rem' }}>
              <thead>
                <tr style={{ textAlign: 'left', borderBottom: '1px solid var(--border)' }}>
                  <th style={{ padding: '6px 8px' }}>Type</th>
                  <th style={{ padding: '6px 8px' }}>Trigger</th>
                  <th style={{ padding: '6px 8px' }}>Typical cause</th>
                </tr>
              </thead>
              <tbody>
                {INCIDENT_HELP.map((row) => (
                  <tr key={row.type} style={{ borderBottom: '1px solid var(--border)' }}>
                    <td style={{ padding: '6px 8px', fontFamily: 'monospace', color: 'var(--chart-line)' }}>{row.type}</td>
                    <td style={{ padding: '6px 8px' }}>{row.trigger}</td>
                    <td style={{ padding: '6px 8px', color: 'var(--text-dim)' }}>{row.cause}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </details>
        )}

        {incidents.length > 0 ? (
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>Started</th>
                  <th>Ended</th>
                  <th>Severity</th>
                  <th>Detail</th>
                </tr>
              </thead>
              <tbody>
                {incidents.map((inc) => (
                  <tr key={inc.incident_id || `${inc.type}:${inc.last_ts}`}>
                    <td className="font-mono">{inc.first_time || '—'}</td>
                    <td className="font-mono">{inc.last_time || '—'}</td>
                    <td>{inc.type || '—'}</td>
                    <td>{inc.detail || inc.summary || inc.gpu_name || '—'}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        ) : (
          <p className="text-text-dim text-center">No incidents recorded.</p>
        )}
      </div>
    </div>
  );
}

export default memo(Incidents);
