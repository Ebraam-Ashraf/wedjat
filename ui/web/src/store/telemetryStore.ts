// App-lifetime owner of the daemon socket and recent live telemetry.
// The daemon sends no snapshot on connect, so all slices start empty and remain
// empty until a real message arrives.

import type { GpuSample, LiveEvent, LiveProcess, TelemetryRecord } from '../types';

export const LIVE_WINDOW_MS = 60_000;
export const DEFAULT_INTERVAL_MS = 500;
export const INITIAL_INTERVAL_MS = 5_000;

export const MIN_STALE_MS = 10_000;

export const MAX_PROCESSES = 200;
export const MAX_AGGREGATES = 200;
export const MAX_EVENTS = 50;

const signatureProcs = (rows: LiveProcess[]) =>
  rows.map((p) => `${p.PID}:${p.VRAMBytes}:${p.VRAMValid}:${p.GPUUUID}`).join('|');
const signatureAggs = (rows: LiveProcess[]) =>
  rows
    .map((a) => `${a.Tgid}:${a.Ordinal}:${a.ApiID}:${a.Count}:${a.Bytes}:${a.LatencySumNs}:${a.LatencyMaxNs}:${a.AllocBytes}:${a.FreeBytes}:${a.Errors}:${a.UvmFaults}:${a.UvmEvicts}`)
    .join('|');

export function staleAfterMs(intervalMs: number | undefined): number {
  const interval = typeof intervalMs === 'number' && Number.isFinite(intervalMs) && intervalMs > 0 ? intervalMs : INITIAL_INTERVAL_MS;
  return Math.max(MIN_STALE_MS, interval * 4);
}

function median(values: number[]): number {
  if (!values.length) return 0;
  const ordered = [...values].sort((a, b) => a - b);
  const middle = Math.floor(ordered.length / 2);
  return ordered.length % 2 ? ordered[middle] : (ordered[middle - 1] + ordered[middle]) / 2;
}

export class TelemetryStore {
  // Public observable state
  latest: GpuSample[] = [];
  procs: LiveProcess[] = [];
  aggs: LiveProcess[] = [];
  events: LiveEvent[] = [];
  socketConnected = false;
  connected = false;
  synthetic = false;
  lastMessageAt = 0;
  lastGpuAt = 0;
  lastGpuSampleTimestampMs = 0;
  gpuMessageSequence = 0;
  stats = {
    messages: 0, rate: 0, avgInterval: 0, minInterval: 0, maxInterval: 0,
    lastReceiveGapMs: 0, lastSampleGapMs: 0, intervals: [] as number[],
  };

  // Internal
  private _gpus = new Map<number, TelemetryRecord>();
  private _order: number[] = [];
  private _listeners = new Map<string, Set<() => void>>();
  private _sigs = new Map<string, string>();
  private _gpuIntervals: number[] = [];
  private _lastLoggedValidityMasks = new Map<number, number>();
  private _rateCount = 0;
  private _rateWindowStart = performance.now();
  private _socket: WebSocket | null = null;
  private _reconnectTimer: ReturnType<typeof setTimeout> | null = null;
  private _running = false;

  get gpuOrder(): number[] { return this._order; }
  get intervalMs(): number { return median(this._gpuIntervals) || INITIAL_INTERVAL_MS; }
  get freshnessThresholdMs(): number { return staleAfterMs(this.intervalMs); }

  subscribe(slice: string, fn: () => void): () => void {
    if (!this._listeners.has(slice)) this._listeners.set(slice, new Set());
    this._listeners.get(slice)!.add(fn);
    return () => this._listeners.get(slice)?.delete(fn);
  }

  private _notify(slice: string): void {
    for (const fn of this._listeners.get(slice) || []) fn();
  }

  private _setSlice(slice: string, value: unknown, signature: string): void {
    if (this._sigs.get(slice) === signature) return;
    this._sigs.set(slice, signature);
    (this as Record<string, unknown>)[slice] = value;
    this._notify(slice);
  }

  setSocketConnected(value: boolean): void {
    if (this.socketConnected === value) return;
    this.socketConnected = value;
    this.connected = value;
    this._notify('connection');
  }

  connect(): () => void {
    this._running = true;
    if (this._socket) return () => this.disconnect();
    const protocol = window.location.protocol === 'https:' ? 'wss' : 'ws';
    const url = `${protocol}://${window.location.host}/socket`;
    console.info('[Wedjat] opening telemetry WebSocket', { url });
    const socket = new WebSocket(url);
    this._socket = socket;

    socket.onopen = () => {
      console.info('[Wedjat] telemetry WebSocket open');
      this.setSocketConnected(true);
    };
    socket.onclose = (event: CloseEvent) => {
      console.warn('[Wedjat] telemetry WebSocket closed', {
        code: event.code,
        reason: event.reason,
        wasClean: event.wasClean,
      });
      if (this._socket === socket) this._socket = null;
      this.setSocketConnected(false);
      if (this._running && !this._reconnectTimer) {
        console.info('[Wedjat] retrying telemetry WebSocket in 2s');
        this._reconnectTimer = setTimeout(() => {
          this._reconnectTimer = null;
          if (this._running) this.connect();
        }, 2000);
      }
    };
    socket.onerror = (event: Event) => {
      console.error('[Wedjat] telemetry WebSocket error', {
        readyState: socket.readyState,
        event,
      });
      this.setSocketConnected(false);
    };
    socket.onmessage = (event: MessageEvent) => {
      let message: unknown;
      try {
        message = JSON.parse(event.data);
      } catch (error) {
        console.error('[Wedjat] invalid telemetry JSON received', {
          error: String(error),
          preview: String(event.data).slice(0, 240),
        });
        return;
      }
      if (message && typeof message === 'object' && 'type' in message) {
        const msg = message as { type: string };
        if (msg.type === 'daemon_disconnected' || msg.type === 'error') {
          console.warn('[Wedjat] daemon status message', message);
          this.setSocketConnected(false);
          return;
        }
      }
      this.ingest(message);
    };

    return () => this.disconnect();
  }

  disconnect(): void {
    this._running = false;
    if (this._reconnectTimer) clearTimeout(this._reconnectTimer);
    this._reconnectTimer = null;
    const socket = this._socket;
    this._socket = null;
    if (socket) socket.close();
    this.setSocketConnected(false);
  }

  ingest(message: unknown): void {
    if (!message || typeof message !== 'object' || !('type' in message)) return;
    const msg = message as {
      type: string;
      timestamp_unix_nano?: number;
      data?: unknown;
    };
    const now = performance.now();
    this.setSocketConnected(true);
    this.lastMessageAt = now;
    this.stats.messages += 1;
    this._rateCount += 1;
    const rateElapsed = now - this._rateWindowStart;
    if (rateElapsed >= 1000) {
      this.stats.rate = this._rateCount * 1000 / rateElapsed;
      this._rateCount = 0;
      this._rateWindowStart = now;
    }

    switch (msg.type) {
      case 'gpu':    return this._ingestGpu(msg, now);
      case 'procs':  return this._ingestProcs(msg);
      case 'agg':    return this._ingestAggs(msg);
      case 'xid':
      case 'event':  return this._ingestEvent(msg);
      default:       return;
    }
  }

  private _ingestGpu(message: {
    timestamp_unix_nano?: number;
    data?: unknown;
  }, now: number): void {
    const gpuRows = Array.isArray(message.data) ? message.data as GpuRawRow[] : [];
    if (!gpuRows.length) return;

    const envelopeTimestampMs = Number(message.timestamp_unix_nano) / 1e6;
    const timestampMs = Number.isFinite(envelopeTimestampMs) && envelopeTimestampMs > 0
      ? envelopeTimestampMs
      : Date.now();

    const receiveGapMs = this.lastGpuAt ? now - this.lastGpuAt : null;
    const previousDaemonTimestampMs = this.lastGpuSampleTimestampMs || null;
    const sampleGapMs = previousDaemonTimestampMs == null
      ? null
      : timestampMs - previousDaemonTimestampMs;

    if (this.lastGpuAt) {
      const intervals = this._gpuIntervals;
      intervals.push(now - this.lastGpuAt);
      if (intervals.length > 20) intervals.shift();
      this.stats.avgInterval = intervals.reduce((sum, v) => sum + v, 0) / intervals.length;
      this.stats.minInterval = Math.min(...intervals);
      this.stats.maxInterval = Math.max(...intervals);
      this.stats.intervals = [...intervals];
    }

    this.lastGpuAt = now;
    this.gpuMessageSequence += 1;
    this.lastGpuSampleTimestampMs = timestampMs;
    this.lastGpuSampleTimestampMs = timestampMs;
    this.stats.lastReceiveGapMs = receiveGapMs ?? 0;
    this.stats.lastSampleGapMs = sampleGapMs ?? 0;

    // ... debug logging and gap detection (same as original, trimmed for brevity)

    for (const gpu of gpuRows) {
      const valid = gpu.ValidFields || 0;
      const requiredChartFields = (1 << 1) | (1 << 3) | (1 << 6) | (1 << 9);
      const previousMask = this._lastLoggedValidityMasks.get(gpu.Index);
      if ((valid & requiredChartFields) !== requiredChartFields && previousMask !== valid) {
        console.warn('[Wedjat] GPU sample is missing chart fields', {
          sequence: this.gpuMessageSequence,
          index: gpu.Index,
          uuid: gpu.UUID,
          validFieldsHex: `0x${Number(valid).toString(16)}`,
          missingFields: [
            !(valid & (1 << 1)) && 'utilization',
            !(valid & (1 << 3)) && 'VRAM',
            !(valid & (1 << 6)) && 'temperature',
            !(valid & (1 << 9)) && 'memory clock',
          ].filter(Boolean),
        });
      }
      this._lastLoggedValidityMasks.set(gpu.Index, valid);

      let record = this._gpus.get(gpu.Index);
      if (!record) {
        record = {
          index: gpu.Index, uuid: gpu.UUID, ring: [], latest: null,
          intervals: [], intervalMs: INITIAL_INTERVAL_MS,
        };
        this._gpus.set(gpu.Index, record);
        this._order.push(gpu.Index);
      }
      record.uuid = gpu.UUID || record.uuid;
      if (record.lastReceivedAt) {
        record.intervals.push(now - record.lastReceivedAt);
        if (record.intervals.length > 20) record.intervals.shift();
        record.intervalMs = median(record.intervals) || INITIAL_INTERVAL_MS;
      }
      record.lastReceivedAt = now;
      record.lastSampleAt = now;
      record.sequence = this.gpuMessageSequence;

      const has = (bit: number) => (valid & (1 << bit)) !== 0;
      const point: GpuSample = {
        index: gpu.Index,
        uuid: gpu.UUID || record.uuid,
        t: timestampMs,
        util: has(1) ? (gpu.UtilGPU ?? null) : null,
        mem_util: has(2) ? (gpu.UtilMem ?? null) : null,
        mem: has(3) ? (gpu.MemUsed == null ? null : gpu.MemUsed / 1_048_576) : null,
        temp: has(6) ? (gpu.TempC ?? null) : null,
        power: has(7) ? (gpu.PowerMW ?? null) : null,
        power_limit: has(11) ? (gpu.PowerLimitMW ?? null) : null,
        clock: has(8) ? (gpu.SMClockMHz ?? null) : null,
        mem_clock: has(9) ? (gpu.MemClockMHz ?? null) : null,
        throttle: has(10) ? (gpu.ThrottleReason ?? null) : null,
        ecc: has(13) ? (gpu.ECCErrors ?? null) : null,
        lastSampleAt: now,
        intervalMs: record.intervalMs,
        sequence: record.sequence,
        sampleTimestamp: timestampMs,
        cadenceMeasured: record.intervals.length > 0,
        freshnessThresholdMs: staleAfterMs(record.intervalMs),
        maxGapMs: Math.max(record.intervalMs * 5, 3000),
      };

      const insertAt = record.ring.findIndex((s) => s.t! > timestampMs);
      if (insertAt < 0) record.ring.push(point);
      else record.ring.splice(insertAt, 0, point);

      const newestTimestamp = Math.max(timestampMs, record.sampleTimestamp || timestampMs);
      const retentionMs = LIVE_WINDOW_MS + Math.max(record.intervalMs * 2, 1_000);
      const pruneBefore = newestTimestamp - retentionMs;
      while (record.ring.length > 1 && record.ring[1].t! < pruneBefore) record.ring.shift();

      if (!record.sampleTimestamp || timestampMs >= record.sampleTimestamp) {
        record.latest = gpu;
        record.sampleTimestamp = timestampMs;
      }
      record.freshnessThresholdMs = staleAfterMs(record.intervalMs);
      record.maxGapMs = Math.max(record.intervalMs * 5, 3000);
    }

    this.latest = this._order.map((index) => {
      const record = this._gpus.get(index)!;
      const gpu = record.latest as GpuRawRow | undefined;
      const valid = gpu?.ValidFields || 0;
      const has = (bit: number) => (valid & (1 << bit)) !== 0;
      return {
        index: record.index, uuid: record.uuid,
        lastSampleAt: record.lastSampleAt,
        sampleTimestamp: record.sampleTimestamp,
        sequence: record.sequence,
        intervalMs: record.intervalMs,
        cadenceMeasured: record.intervals.length > 0,
        freshnessThresholdMs: record.freshnessThresholdMs,
        maxGapMs: record.maxGapMs,
        util: has(1) ? (gpu?.UtilGPU ?? null) : null,
        mem_util: has(2) ? (gpu?.UtilMem ?? null) : null,
        mem: has(3) ? (gpu?.MemUsed ?? null) : null,
        temp: has(6) ? (gpu?.TempC ?? null) : null,
        power: has(7) ? (gpu?.PowerMW ?? null) : null,
        power_limit: has(11) ? (gpu?.PowerLimitMW ?? null) : null,
        clock: has(8) ? (gpu?.SMClockMHz ?? null) : null,
        mem_clock: has(9) ? (gpu?.MemClockMHz ?? null) : null,
        throttle: has(10) ? (gpu?.ThrottleReason ?? null) : null,
        ecc: has(13) ? (gpu?.ECCErrors ?? null) : null,
      } as GpuSample;
    });

    this._notify('gpus');
  }

  private _ingestProcs(message: { data?: unknown }): void {
    const data = message.data as { Procs?: LiveProcess[] } | undefined;
    const rows = (data?.Procs || []).slice(0, MAX_PROCESSES);
    this._setSlice('procs', rows, signatureProcs(rows));
  }

  private _ingestAggs(message: { data?: unknown }): void {
    const rows = (Array.isArray(message.data) ? message.data : []).slice(0, MAX_AGGREGATES) as LiveProcess[];
    this._setSlice('aggs', rows, signatureAggs(rows));
  }

  private _ingestEvent(message: { type: string; timestamp_unix_nano?: number; data?: unknown }): void {
    const data = message.data as Record<string, unknown> | undefined;
    const evt: LiveEvent = {
      id: `${message.type}:${message.timestamp_unix_nano}:${data?.Tgid ?? ''}:${data?.Code ?? ''}:${this.stats.messages}`,
      type: message.type,
      data: data,
      ts: message.timestamp_unix_nano ?? 0,
    };
    this.events = [evt, ...this.events].slice(0, MAX_EVENTS);
    this._sigs.delete('events');
    this._notify('events');
  }

  gpu(index: number): TelemetryRecord | undefined {
    return this._gpus.get(Number(index));
  }

  maxGapMs(): number {
    return Math.max(this.intervalMs * 5, 3000);
  }
}

interface GpuRawRow {
  Index: number;
  UUID: string;
  UtilGPU?: number | null;
  UtilMem?: number | null;
  MemUsed?: number | null;
  TempC?: number | null;
  PowerMW?: number | null;
  PowerLimitMW?: number | null;
  SMClockMHz?: number | null;
  MemClockMHz?: number | null;
  ThrottleReason?: number | null;
  ECCErrors?: number | null;
  ValidFields?: number;
}
