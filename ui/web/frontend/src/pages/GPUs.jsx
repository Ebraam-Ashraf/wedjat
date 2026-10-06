import React, { useEffect, useState } from 'react';
import { apiGet } from '../api.js';

function GPUs() {
  const [gpus, setGpus] = useState([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState(null);

  useEffect(() => {
    apiGet('/api/gpus')
      .then(setGpus)
      .catch(e => setError(e.message))
      .finally(() => setLoading(false));
  }, []);

  if (loading) return <div style={{ padding: '24px' }}>Loading GPUs...</div>;
  if (error) return <div style={{ padding: '24px', color: 'var(--accent-danger)' }}>Error: {error}</div>;

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: '24px' }}>
      <div>
        <h1 style={{ margin: 0 }}>Registered GPUs</h1>
        <p style={{ color: 'var(--text-muted)', marginTop: '8px' }}>All NVIDIA devices ever observed by the daemon on this host.</p>
      </div>

      <div className="glass-panel" style={{ padding: '1px' }}>
        <div className="table-container">
          <table>
            <thead>
              <tr>
                <th>Index</th>
                <th>Name</th>
                <th>UUID</th>
                <th>PCI Bus</th>
                <th style={{ textAlign: 'right' }}>VRAM Total</th>
                <th>Driver</th>
                <th>First Seen</th>
                <th>Last Seen</th>
              </tr>
            </thead>
            <tbody>
              {gpus.length === 0 ? (
                <tr><td colSpan={8} style={{ textAlign: 'center', padding: '24px', color: 'var(--text-muted)' }}>No GPUs found in database. Start the dev daemon.</td></tr>
              ) : (
                gpus.map(g => (
                  <tr key={g.id}>
                    <td><div style={{ width: '24px', height: '24px', borderRadius: '4px', background: 'var(--accent-primary)', color: 'white', display: 'flex', alignItems: 'center', justifyContent: 'center', fontWeight: 'bold' }}>{g.index}</div></td>
                    <td style={{ fontWeight: 600 }}>{g.name}</td>
                    <td style={{ fontFamily: 'monospace', fontSize: '0.85em', color: 'var(--text-muted)' }}>{g.uuid}</td>
                    <td style={{ fontFamily: 'monospace', fontSize: '0.85em' }}>{g.pci_bus_id}</td>
                    <td style={{ textAlign: 'right', fontWeight: 500 }}>
                      {g.vram_total_bytes ? `${(g.vram_total_bytes / 1073741824).toFixed(1)} GB` : 'N/A'}
                    </td>
                    <td>{g.driver_version}</td>
                    <td style={{ color: 'var(--text-muted)', fontSize: '0.9em' }}>{g.first_seen}</td>
                    <td style={{ color: 'var(--text-muted)', fontSize: '0.9em' }}>{g.last_seen}</td>
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

export default GPUs;
