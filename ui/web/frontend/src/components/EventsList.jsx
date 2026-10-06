import React, { memo } from 'react';
import { useStoreValue } from '../hooks/useStoreValue.js';

// Recent events list.
//
// Events are rare, so this is the one section that can afford to re-render
// whenever it changes. Rows are keyed by a stable per-event id rather than by
// index, so prepending a new event does not remount the other 49 rows.

const nsToDate = (ns) => new Date(ns / 1e6);

function EventsList({ store }) {
  const events = useStoreValue(store, 'events', (s) => s.events);

  return (
    <div className="glass-panel" style={{ padding: 24 }}>
      <h3 style={{ margin: '0 0 16px' }}>Recent Events</h3>
      <div style={{ maxHeight: 280, overflowY: 'auto', display: 'flex', flexDirection: 'column', gap: 8 }}>
        {events.length > 0 ? events.map((ev) => (
          <div key={ev.id} style={{
            padding: 12, background: 'rgba(0,0,0,0.2)', borderRadius: 8,
            borderLeft: `3px solid ${ev.type === 'xid' ? 'var(--accent-danger)' : 'var(--accent-primary)'}`,
          }}>
            <div style={{ display: 'flex', justifyContent: 'space-between', marginBottom: 4 }}>
              <strong style={{ color: ev.type === 'xid' ? 'var(--accent-danger)' : 'var(--accent-primary)' }}>
                {ev.type === 'xid' ? 'Xid Error' : 'eBPF Event'}
              </strong>
              <span style={{ fontSize: '0.82rem', color: 'var(--text-muted)' }}>
                {ev.ts ? nsToDate(ev.ts).toLocaleTimeString() : '-'}
              </span>
            </div>
            <div style={{ fontSize: '0.88rem', fontFamily: 'monospace' }}>
              {ev.type === 'xid'
                ? `GPU ${ev.data?.Index} | Code: ${ev.data?.Code}`
                : `TGID: ${ev.data?.Tgid} | API: ${ev.data?.ApiID} | TID: ${ev.data?.Tid} | Bytes: ${ev.data?.Bytes} | Latency: ${ev.data?.LatencyNs}ns | Device: ${ev.data?.DeviceOrdinal} | Flags: 0x${ev.data?.Flags.toString(16)} | Status: ${ev.data?.Status}`}
            </div>
          </div>
        )) : (
          <div style={{ color: 'var(--text-muted)', textAlign: 'center', padding: 24 }}>No recent events.</div>
        )}
      </div>
    </div>
  );
}

export default memo(EventsList);