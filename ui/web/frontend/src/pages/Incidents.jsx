import React, { useEffect, useState } from 'react';
import { apiGet } from '../api';

function Incidents() {
  const [incidents, setIncidents] = useState([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState(null);

  useEffect(() => {
    apiGet('/api/incidents?limit=100')
      .then(setIncidents)
      .catch(e => setError(e.message))
      .finally(() => setLoading(false));
  }, []);

  if (loading) return <div style={{ padding: '24px' }}>Loading incidents...</div>;
  if (error) return <div style={{ padding: '24px', color: 'var(--accent-danger)' }}>Error: {error}</div>;

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: '24px' }}>
      <div>
        <h1 style={{ margin: 0 }}>Incidents History</h1>
        <p style={{ color: 'var(--text-muted)', margin: '8px 0 0 0' }}>Xid errors and CUDA sync hangs/stalls recorded over time.</p>
      </div>

      <div className="glass-panel" style={{ padding: '1px' }}>
        <div className="table-container">
          <table>
            <thead>
              <tr>
                <th>Last Time (UTC)</th>
                <th>Type</th>
                <th>Process</th>
                <th>GPU</th>
                <th style={{ textAlign: 'right' }}>Occurrences</th>
                <th>Summary</th>
                <th>Details</th>
              </tr>
            </thead>
            <tbody>
              {incidents.length === 0 ? (
                <tr><td colSpan={7} style={{ textAlign: 'center', padding: '24px', color: 'var(--text-muted)' }}>No incidents found.</td></tr>
              ) : (
                incidents.map((i, idx) => (
                  <tr key={idx}>
                    <td style={{ color: 'var(--text-muted)', fontSize: '0.9em' }}>
                      {i.last_time}
                      {i.first_time !== i.last_time && <div style={{ fontSize: '0.85em', color: 'rgba(255,255,255,0.3)' }}>First: {i.first_time}</div>}
                    </td>
                    <td>
                      <span style={{ 
                        background: 'rgba(239, 68, 68, 0.1)', 
                        color: 'var(--accent-danger)',
                        padding: '2px 6px',
                        borderRadius: '4px',
                        fontSize: '0.85em',
                        fontWeight: 600,
                        textTransform: 'uppercase'
                      }}>
                        {i.type}
                      </span>
                    </td>
                    <td>{i.command ? `${i.command} (${i.tgid})` : '-'}</td>
                    <td>{i.gpu_name || '-'}</td>
                    <td style={{ textAlign: 'right', fontWeight: 600 }}>{i.occurrences}</td>
                    <td style={{ fontWeight: 500 }}>{i.summary}</td>
                    <td style={{ fontSize: '0.9em', color: 'var(--text-muted)' }}>{i.detail}</td>
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

export default Incidents;
