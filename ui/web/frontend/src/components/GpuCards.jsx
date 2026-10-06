import React, { memo } from 'react';
import { useStoreValue } from '../hooks/useStoreValue.js';
import { formatBytes } from '../api.js';

// GPU cards.
//
// Each card is memoised on primitive props, so when one GPU's temperature moves
// only that card re-renders. On an idle machine none of them re-render at all,
// because the store gates the notification on the values having changed.

function GpuCard({ index, uuid, util, mem, mem_util, temp, power, power_limit, clock, mem_clock, throttle, ecc }) {
  return (
    <div className="glass-panel" style={{ padding: 24 }}>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start', marginBottom: 20 }}>
        <div>
          <h3 style={{ margin: 0, color: 'var(--accent-primary)' }}>GPU {index}</h3>
          <div style={{ fontSize: '0.8rem', color: 'var(--text-muted)', fontFamily: 'monospace', marginTop: 4 }}>{uuid}</div>
        </div>
        <div style={{ fontSize: '1.5rem', fontWeight: 700, color: util > 80 ? 'var(--accent-danger)' : 'var(--text-main)' }}>
          {util == null ? 'N/A' : `${util}%`}
        </div>
      </div>
      <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 16 }}>
        <div><div style={{ color: 'var(--text-muted)', fontSize: '0.85rem' }}>Memory %</div><b>{mem_util == null ? 'N/A' : `${mem_util}%`}</b></div>
        <div><div style={{ color: 'var(--text-muted)', fontSize: '0.85rem' }}>Memory Used</div><b>{mem == null ? 'N/A' : formatBytes(mem)}</b></div>
        <div><div style={{ color: 'var(--text-muted)', fontSize: '0.85rem' }}>Temperature</div><b style={{ color: temp > 80 ? 'var(--accent-danger)' : 'inherit' }}>{temp == null ? 'N/A' : `${temp}°C`}</b></div>
        <div><div style={{ color: 'var(--text-muted)', fontSize: '0.85rem' }}>Power</div><b>{power == null ? 'N/A' : `${(power / 1000).toFixed(1)} W`}</b></div>
        <div><div style={{ color: 'var(--text-muted)', fontSize: '0.85rem' }}>Power Limit</div><b>{power_limit == null ? 'N/A' : `${(power_limit / 1000).toFixed(1)} W`}</b></div>
        <div><div style={{ color: 'var(--text-muted)', fontSize: '0.85rem' }}>SM Clock</div><b>{clock == null ? 'N/A' : `${clock} MHz`}</b></div>
        <div><div style={{ color: 'var(--text-muted)', fontSize: '0.85rem' }}>Mem Clock</div><b>{mem_clock == null ? 'N/A' : `${mem_clock} MHz`}</b></div>
        <div><div style={{ color: 'var(--text-muted)', fontSize: '0.85rem' }}>Throttle</div><b>{throttle == null ? 'N/A' : throttle}</b></div>
        <div><div style={{ color: 'var(--text-muted)', fontSize: '0.85rem' }}>ECC Errors</div><b>{ecc == null ? 'N/A' : ecc}</b></div>
      </div>
    </div>
  );
}

const MemoGpuCard = memo(GpuCard);

function GpuCards({ store }) {
  const gpus = useStoreValue(store, 'gpus', (s) => s.latest);

  if (gpus.length === 0) {
    return (
      <div className="glass-panel" style={{ padding: 24, textAlign: 'center', color: 'var(--text-muted)', gridColumn: '1/-1' }}>
        Waiting for GPU telemetry...
      </div>
    );
  }

  return (
    <>
      {gpus.map((g) => (
        <MemoGpuCard
          key={g.index}
          index={g.index}
          uuid={g.uuid}
          util={g.util}
          mem={g.mem}
          mem_util={g.mem_util}
          temp={g.temp}
          power={g.power}
          power_limit={g.power_limit}
          clock={g.clock}
          mem_clock={g.mem_clock}
          throttle={g.throttle}
          ecc={g.ecc}
        />
      ))}
    </>
  );
}

export default memo(GpuCards);