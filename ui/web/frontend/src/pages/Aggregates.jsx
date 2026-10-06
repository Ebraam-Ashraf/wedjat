import React, { useEffect, useState } from 'react';
import { apiGet, formatBytes } from '../api.js';

function Aggregates() {
  const [aggs, setAggs] = useState([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState(null);

  useEffect(() => {
    apiGet('/api/aggregates?limit=100')
      .then(setAggs)
      .catch(e => setError(e.message))
      .finally(() => setLoading(false));
  }, []);

  if (loading) return <div style={{ padding: '24px' }}>Loading aggregates...</div>;
  if (error) return <div style={{ padding: '24px', color: 'var(--accent-danger)' }}>Error: {error}</div>;

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: '24px' }}>
      <div>
        <h1 style={{ margin: 0 }}>eBPF Aggregates History</h1>
        <p style={{ color: 'var(--text-muted)', margin: '8px 0 0 0' }}>Historical CUDA/UVM operation aggregates by minute.</p>
      </div>

      <div className="glass-panel" style={{ padding: '1px' }}>
        <div className="table-container">
          <table>
            <thead>
              <tr>
                <th>Time (UTC)</th>
                <th>Process</th>
                <th>GPU</th>
                <th style={{ textAlign: 'right' }}>Launches</th>
                <th style={{ textAlign: 'right' }}>Memcpy (Calls)</th>
                <th style={{ textAlign: 'right' }}>Memcpy (Bytes)</th>
                <th style={{ textAlign: 'right' }}>Sync (Calls)</th>
                <th style={{ textAlign: 'right' }}>Faults</th>
                <th style={{ textAlign: 'right' }}>Errors</th>
              </tr>
            </thead>
            <tbody>
              {aggs.length === 0 ? (
                <tr><td colSpan={9} style={{ textAlign: 'center', padding: '24px', color: 'var(--text-muted)' }}>No aggregates found for today.</td></tr>
              ) : (
                aggs.map((a, i) => (
                  <tr key={i}>
                    <td style={{ color: 'var(--text-muted)', fontSize: '0.9em' }}>{a.time}</td>
                    <td>{a.command || 'Unknown'} <span style={{ color: 'var(--text-muted)', fontSize: '0.85em', fontFamily: 'monospace' }}>({a.tgid})</span></td>
                    <td>{a.gpu_name || 'N/A'}</td>
                    <td style={{ textAlign: 'right', fontWeight: 500 }}>{a.launches}</td>
                    <td style={{ textAlign: 'right' }}>{a.memcpy_calls}</td>
                    <td style={{ textAlign: 'right' }}>{formatBytes(a.memcpy_bytes)}</td>
                    <td style={{ textAlign: 'right' }}>{a.sync_calls}</td>
                    <td style={{ textAlign: 'right', color: a.uvm_faults > 0 ? 'var(--accent-danger)' : 'inherit' }}>{a.uvm_faults}</td>
                    <td style={{ textAlign: 'right', color: a.errors > 0 ? 'var(--accent-danger)' : 'inherit' }}>{a.errors}</td>
                  </tr>
                ))
              )}
            </tbody>
          </table>
        </div>
      </div>
    </div>
  );
}

export default Aggregates;
