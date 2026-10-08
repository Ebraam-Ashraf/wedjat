import { memo } from 'react';
import { useStoreValue } from '../hooks/useStoreValue';
import type { TelemetryStoreLike, LiveEvent } from '../types';

const nsToDate = (ns: number): Date => new Date(ns / 1e6);

function EventsList({ store }: { store: TelemetryStoreLike }) {
  const events = useStoreValue(store, 'events', (s) => s.events);

  return (
    <div className="glass-panel" style={{ padding: 24 }}>
      <h3 style={{ margin: '0 0 16px' }}>Recent Events</h3>
      <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
        {events.length > 0
          ? events.map((ev: LiveEvent) => (
              <div
                key={ev.id}
                style={{
                  padding: 12, background: 'rgba(0,0,0,0.2)', borderRadius: 8,
                  borderLeft: `3px solid ${ev.type === 'xid' ? 'var(--critical)' : 'var(--chart-line)'}`,
                }}
              >
                <div style={{ display: 'flex', justifyContent: 'space-between', marginBottom: 4 }}>
                  <strong style={{ color: ev.type === 'xid' ? 'var(--critical)' : 'var(--chart-line)' }}>
                    {ev.type === 'xid' ? 'Xid Error' : 'eBPF Event'}
                  </strong>
                  <span style={{ fontSize: '0.82rem', color: 'var(--text-dim)' }}>
                    {ev.ts ? nsToDate(ev.ts).toLocaleTimeString() : '-'}
                  </span>
                </div>
                <div style={{ fontSize: '0.88rem', fontFamily: 'monospace' }}>
                  {ev.type === 'xid'
                    ? `GPU ${(ev.data?.Index as number | undefined)} | Code: ${(ev.data?.Code as number | undefined)}`
                    : `TGID: ${(ev.data?.Tgid as number | undefined)} | API: ${(ev.data?.ApiID as number | undefined)} | TID: ${(ev.data?.Tid as number | undefined)} | Bytes: ${(ev.data?.Bytes as number | undefined)} | Latency: ${(ev.data?.LatencyNs as number | undefined)}ns | Device: ${(ev.data?.DeviceOrdinal as number | undefined)} | Flags: 0x${(ev.data?.Flags as number | undefined)?.toString(16)} | Status: ${(ev.data?.Status as number | undefined)}`}
                </div>
              </div>
            ))
          : (
            <div style={{ color: 'var(--text-dim)', textAlign: 'center', padding: 24 }}>
              No recent events.
            </div>
          )}
      </div>
    </div>
  );
}

export default memo(EventsList);
