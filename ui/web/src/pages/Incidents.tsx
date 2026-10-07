import { memo, useEffect } from 'react';
import type { Incident } from '../types';
import { useApiData } from '../hooks/useApiData';

interface IncidentsProps {
  store?: unknown;
}

function Incidents(_props: IncidentsProps) {
  const { data, loading, error } = useApiData<Incident[]>('/api/incidents?limit=100', { refreshIntervalMs: 15000 });
  const incidents = data || [];

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
      <h1>Incidents</h1>
      <div className="glass-panel rounded-xl p-6">
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
