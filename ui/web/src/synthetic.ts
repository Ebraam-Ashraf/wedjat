// Synthetic data feeder for ?synthetic=1.
//
// The live feed on an idle machine is flat: util pinned at 0 and VRAM pinned at
// Xorg's allocation. A flat line cannot demonstrate whether the interpolation is
// smooth, so this drives the same store at the same 2Hz cadence with values that
// actually move.
//
// Shaped deliberately to expose renderer bugs:
//   util - two sine waves beating against each other plus noise, so the line has
//          both slow trends and per-sample jitter and any 2Hz stepping is obvious
//   mem  - a sawtooth ramp, so every sample differs from the last and the head of
//          the line is always moving
//
// It writes through the store's normal ingest path, so the renderer, the
// interval stats and the debug overlay all behave exactly as they do live.

import type { TelemetryStoreLike } from './types';

const DEFAULT_INTERVAL_MS = 500;

function makeNoise(seed: number) {
  let s = seed >>> 0;
  return () => {
    s = (s * 1664525 + 1013904223) >>> 0;
    return s / 0xffffffff;
  };
}

export function startSynthetic(
  store: TelemetryStoreLike,
  { gpus = 2, intervalMs = DEFAULT_INTERVAL_MS }: { gpus?: number; intervalMs?: number } = {},
): () => void {
  const noise = makeNoise(0x5eed);
  let tick = 0;
  let stopped = false;

  const emit = () => {
    if (stopped) return;
    tick++;

    const samples = [];
    for (let i = 0; i < gpus; i++) {
      const phase = (i * Math.PI) / 3;
      const slow = Math.sin((tick / 12) + phase);
      const fast = Math.sin(tick / 1.7 + phase * 2);
      const jitter = (noise() - 0.5) * 6;

      const util = Math.max(0, Math.min(100, Math.round(45 + 38 * slow + 10 * fast + jitter)));
      const mem = Math.round(180 + ((tick * 21 + i * 130) % 620));

      samples.push({
        Index: i,
        UUID: `GPU-SYNTHETIC-${i}`,
        UtilGPU: util,
        MemUsed: mem * 1_048_576,
        TempC: Math.round(45 + 20 * slow),
        PowerMW: Math.round(20000 + 25000 * slow),
        SMClockMHz: Math.round(900 + 700 * fast),
        ValidFields: (1 << 1) | (1 << 3) | (1 << 6) | (1 << 7) | (1 << 8),
        Valid: true,
      });
    }

    store.ingest({
      type: 'gpu',
      timestamp_unix_nano: Math.round(performance.now() * 1e6),
      data: samples,
    });

    store.ingest({
      type: 'procs',
      timestamp_unix_nano: Math.round(performance.now() * 1e6),
      data: {
        Procs: Array.from({ length: 4 }, (_, k) => ({
          PID: 1000 + ((tick + k * 3) % 12),
          GPUUUID: `GPU-SYNTHETIC-${k % gpus}`,
          VRAMBytes: (120 + ((tick * 7 + k * 40) % 500)) * 1_048_576,
          VRAMValid: true,
        })),
        Complete: true,
      },
    });
  };

  for (let i = 0; i < 32; i++) emit();

  const id = setInterval(emit, intervalMs);
  store.synthetic = true;

  return () => {
    stopped = true;
    clearInterval(id);
    store.synthetic = false;
  };
}

export default startSynthetic;
