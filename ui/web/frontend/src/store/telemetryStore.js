// App-lifetime owner of the daemon socket and recent live telemetry.
// The daemon sends no snapshot on connect, so all slices start empty and remain
// empty until a real message arrives.

import { DEBUG } from '../flags.js';

export const LIVE_WINDOW_MS = 60_000;
export const DEFAULT_INTERVAL_MS = 500;
export const INITIAL_INTERVAL_MS = 5_000;
const GPU_GAP_LOG_THRESHOLD_MS = 1_500;
// Allow brief NVML/socket scheduling stalls without flipping the whole UI to
// stale. The page still shows the exact age of the last real sample.
export const MIN_STALE_MS = 10_000;

export function staleAfterMs(intervalMs) {
  const interval = Number.isFinite(intervalMs) && intervalMs > 0 ? intervalMs : INITIAL_INTERVAL_MS;
  return Math.max(MIN_STALE_MS, interval * 4);
}

function median(values) {
  if (!values.length) return 0;
  const ordered = [...values].sort((a, b) => a - b);
  const middle = Math.floor(ordered.length / 2);
  return ordered.length % 2 ? ordered[middle] : (ordered[middle - 1] + ordered[middle]) / 2;
}
export const MAX_PROCESSES = 200;
export const MAX_AGGREGATES = 200;
export const MAX_EVENTS = 50;

const signatureProcs = (rows) => rows.map((p) => `${p.PID}:${p.VRAMBytes}:${p.VRAMValid}:${p.GPUUUID}`).join('|');
const signatureAggs = (rows) => rows.map((a) => `${a.Tgid}:${a.Ordinal}:${a.ApiID}:${a.Count}:${a.Bytes}:${a.LatencySumNs}:${a.LatencyMaxNs}:${a.AllocBytes}:${a.FreeBytes}:${a.Errors}:${a.UvmFaults}:${a.UvmEvicts}`).join('|');

export class TelemetryStore {
  constructor() {
    this._gpus = new Map();
    this._order = [];
    this.latest = [];
    this.procs = [];
    this.aggs = [];
    this.events = [];
    this.socketConnected = false;
    this.connected = false;
    this.synthetic = false;
    this.lastMessageAt = 0;
    this.lastGpuAt = 0;
    this.lastGpuSampleTimestampMs = 0;
    this.gpuMessageSequence = 0;
    this.stats = {
      messages: 0, rate: 0, avgInterval: 0, minInterval: 0, maxInterval: 0,
      lastReceiveGapMs: 0, lastSampleGapMs: 0, intervals: [],
    };
    this._listeners = new Map();
    this._sigs = new Map();
    this._gpuIntervals = [];
    this._lastGpuAt = 0;
    this._lastGpuSampleTimestampMs = 0;
    this._lastLoggedValidityMasks = new Map();
    this._rateCount = 0;
    this._rateWindowStart = performance.now();
    this._socket = null;
    this._running = false;
    this._reconnectTimer = null;
  }

  subscribe(slice, fn) {
    if (!this._listeners.has(slice)) this._listeners.set(slice, new Set());
    this._listeners.get(slice).add(fn);
    return () => this._listeners.get(slice)?.delete(fn);
  }

  _notify(slice) {
    for (const fn of this._listeners.get(slice) || []) fn();
  }

  _setSlice(slice, value, signature) {
    if (this._sigs.get(slice) === signature) return;
    this._sigs.set(slice, signature);
    this[slice] = value;
    this._notify(slice);
  }

  setSocketConnected(value) {
    if (this.socketConnected === value) return;
    this.socketConnected = value;
    this.connected = value;
    this._notify('connection');
  }

  connect() {
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
    socket.onclose = (event) => {
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
    socket.onerror = (event) => {
      console.error('[Wedjat] telemetry WebSocket error', {
        readyState: socket.readyState,
        event,
      });
      this.setSocketConnected(false);
    };
    socket.onmessage = (event) => {
      let message;
      try {
        message = JSON.parse(event.data);
      } catch (error) {
        console.error('[Wedjat] invalid telemetry JSON received', {
          error: String(error),
          preview: String(event.data).slice(0, 240),
        });
        return;
      }
      if (message.type === 'daemon_disconnected' || message.type === 'error') {
        console.warn('[Wedjat] daemon status message', message);
        this.setSocketConnected(false);
        return;
      }
      this.ingest(message);
    };
    return () => this.disconnect();
  }

  disconnect() {
    this._running = false;
    if (this._reconnectTimer) clearTimeout(this._reconnectTimer);
    this._reconnectTimer = null;
    const socket = this._socket;
    this._socket = null;
    if (socket) socket.close();
    this.setSocketConnected(false);
  }

  ingest(message) {
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
    switch (message.type) {
      case 'gpu': return this._ingestGpu(message, now);
      case 'procs': return this._ingestProcs(message);
      case 'agg': return this._ingestAggs(message);
      case 'xid':
      case 'event': return this._ingestEvent(message);
      default: return;
    }
  }

  _ingestGpu(message, now) {
    const gpuRows = Array.isArray(message.data) ? message.data : [];
    if (!gpuRows.length) return;
    const envelopeTimestampMs = Number(message.timestamp_unix_nano) / 1e6;
    const timestampMs = Number.isFinite(envelopeTimestampMs) && envelopeTimestampMs > 0
      ? envelopeTimestampMs
      : Date.now();
    const receiveGapMs = this._lastGpuAt ? now - this._lastGpuAt : null;
    const previousDaemonTimestampMs = this._lastGpuSampleTimestampMs || null;
    const sampleGapMs = previousDaemonTimestampMs == null
      ? null
      : timestampMs - previousDaemonTimestampMs;

    if (this._lastGpuAt) {
      const intervals = this._gpuIntervals;
      intervals.push(now - this._lastGpuAt);
      if (intervals.length > 20) intervals.shift();
      this.stats.avgInterval = intervals.reduce((sum, value) => sum + value, 0) / intervals.length;
      this.stats.minInterval = Math.min(...intervals);
      this.stats.maxInterval = Math.max(...intervals);
      this.stats.intervals = [...intervals];
    }
    this._lastGpuAt = now;
    this.lastGpuAt = now;
    this.gpuMessageSequence += 1;
    this._lastGpuSampleTimestampMs = timestampMs;
    this.lastGpuSampleTimestampMs = timestampMs;
    this.stats.lastReceiveGapMs = receiveGapMs || 0;
    this.stats.lastSampleGapMs = sampleGapMs || 0;

    if (DEBUG) {
      console.debug('[Wedjat] GPU sample received', {
        sequence: this.gpuMessageSequence,
        receiveGapMs: receiveGapMs == null ? null : Math.round(receiveGapMs),
        daemonSampleGapMs: sampleGapMs == null ? null : Math.round(sampleGapMs),
        daemonSampleAgeMs: Math.round(Date.now() - timestampMs),
        rawEnvelopeTimestampUnixNano: message.timestamp_unix_nano ?? null,
        daemonTimestampMs: timestampMs,
        gpuRows: gpuRows.map((gpu) => ({
          index: gpu.Index,
          uuid: gpu.UUID,
          rowTimestampMs: Number(gpu.TsNano) / 1e6 || null,
          rowTimestampOffsetMs: Number.isFinite(Number(gpu.TsNano))
            ? Math.round(Number(gpu.TsNano) / 1e6 - timestampMs)
            : null,
          validFieldsHex: `0x${Number(gpu.ValidFields || 0).toString(16)}`,
          util: gpu.UtilGPU,
          tempC: gpu.TempC,
          memUsedBytes: gpu.MemUsed,
          smClockMHz: gpu.SMClockMHz,
          memClockMHz: gpu.MemClockMHz,
        })),
      });
    }

    if ((receiveGapMs != null && receiveGapMs >= GPU_GAP_LOG_THRESHOLD_MS) ||
        (sampleGapMs != null && (sampleGapMs >= GPU_GAP_LOG_THRESHOLD_MS || sampleGapMs <= 0))) {
      console.warn('[Wedjat] GPU telemetry gap or timestamp anomaly', {
        sequence: this.gpuMessageSequence,
        receiveGapMs: receiveGapMs == null ? null : Math.round(receiveGapMs),
        daemonSampleGapMs: sampleGapMs == null ? null : Math.round(sampleGapMs),
        daemonSampleAgeMs: Math.round(Date.now() - timestampMs),
        previousDaemonTimestampMs,
        rawEnvelopeTimestampUnixNano: message.timestamp_unix_nano ?? null,
        daemonTimestampMs: timestampMs,
        socketReadyState: this._socket?.readyState ?? null,
        visibility: document.visibilityState,
        gpuRows: gpuRows.map((gpu) => ({
          index: gpu.Index,
          uuid: gpu.UUID,
          rowTimestampMs: Number(gpu.TsNano) / 1e6 || null,
          rowTimestampOffsetMs: Number.isFinite(Number(gpu.TsNano))
            ? Math.round(Number(gpu.TsNano) / 1e6 - timestampMs)
            : null,
          validFieldsHex: `0x${Number(gpu.ValidFields || 0).toString(16)}`,
          util: gpu.UtilGPU,
          tempC: gpu.TempC,
          memUsedBytes: gpu.MemUsed,
          smClockMHz: gpu.SMClockMHz,
          memClockMHz: gpu.MemClockMHz,
        })),
      });
    }

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
        record = { index: gpu.Index, uuid: gpu.UUID, ring: [], latest: null, intervals: [], intervalMs: INITIAL_INTERVAL_MS };
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
      const has = (bit) => (valid & (1 << bit)) !== 0;
      const point = {
        t: timestampMs,
        util: has(1) ? (gpu.UtilGPU ?? null) : null,
        mem_util: has(2) ? (gpu.UtilMem ?? null) : null,
        mem: has(3) ? (gpu.MemUsed == null ? null : gpu.MemUsed / 1048576) : null,
        temp: has(6) ? (gpu.TempC ?? null) : null,
        power: has(7) ? (gpu.PowerMW ?? null) : null,
        power_limit: has(11) ? (gpu.PowerLimitMW ?? null) : null,
        clock: has(8) ? (gpu.SMClockMHz ?? null) : null,
        mem_clock: has(9) ? (gpu.MemClockMHz ?? null) : null,
        throttle: has(10) ? (gpu.ThrottleReason ?? null) : null,
      };
      const insertAt = record.ring.findIndex((sample) => sample.t > timestampMs);
      if (insertAt < 0) record.ring.push(point);
      else record.ring.splice(insertAt, 0, point);
      const newestTimestamp = Math.max(timestampMs, record.sampleTimestamp || timestampMs);
      const retentionMs = LIVE_WINDOW_MS + Math.max(record.intervalMs * 2, 1_000);
      const pruneBefore = newestTimestamp - retentionMs;
      while (record.ring.length > 1 && record.ring[1].t < pruneBefore) record.ring.shift();
      if (!record.sampleTimestamp || timestampMs >= record.sampleTimestamp) {
        record.latest = gpu;
        record.sampleTimestamp = timestampMs;
      }
      record.freshnessThresholdMs = staleAfterMs(record.intervalMs);
      // Bridge brief delivery jitter so graphs look continuous; longer gaps
      // still break the line rather than implying telemetry that never arrived.
      record.maxGapMs = Math.max(record.intervalMs * 5, 3000);
    }

    this.latest = this._order.map((index) => {
      const record = this._gpus.get(index);
      const gpu = record.latest;
      const valid = gpu.ValidFields || 0;
      const has = (bit) => (valid & (1 << bit)) !== 0;
      return {
        index: record.index, uuid: record.uuid,
        lastSampleAt: record.lastSampleAt,
        sampleTimestamp: record.sampleTimestamp,
        sequence: record.sequence,
        intervalMs: record.intervalMs,
        cadenceMeasured: record.intervals.length > 0,
        freshnessThresholdMs: record.freshnessThresholdMs,
        maxGapMs: record.maxGapMs,
        util: has(1) ? (gpu.UtilGPU ?? null) : null,
        mem_util: has(2) ? (gpu.UtilMem ?? null) : null,
        mem: has(3) ? (gpu.MemUsed ?? null) : null,
        temp: has(6) ? (gpu.TempC ?? null) : null,
        power: has(7) ? (gpu.PowerMW ?? null) : null,
        power_limit: has(11) ? (gpu.PowerLimitMW ?? null) : null,
        clock: has(8) ? (gpu.SMClockMHz ?? null) : null,
        mem_clock: has(9) ? (gpu.MemClockMHz ?? null) : null,
        throttle: has(10) ? (gpu.ThrottleReason ?? null) : null,
        ecc: has(13) ? (gpu.ECCErrors ?? null) : null,
      };
    });
    // Notify on every sample so sparklines retain repeated values over time.
    this._notify('gpus');
  }

  _ingestProcs(message) {
    const rows = (message.data?.Procs || []).slice(0, MAX_PROCESSES);
    this._setSlice('procs', rows, signatureProcs(rows));
  }

  _ingestAggs(message) {
    const rows = (message.data || []).slice(0, MAX_AGGREGATES);
    this._setSlice('aggs', rows, signatureAggs(rows));
  }

  _ingestEvent(message) {
    const event = {
      id: `${message.type}:${message.timestamp_unix_nano}:${message.data?.Tgid ?? ''}:${message.data?.Code ?? ''}:${this.stats.messages}`,
      type: message.type,
      data: message.data,
      ts: message.timestamp_unix_nano,
    };
    this.events = [event, ...this.events].slice(0, MAX_EVENTS);
    this._sigs.delete('events');
    this._notify('events');
  }

  get gpuOrder() { return this._order; }
  gpu(index) { return this._gpus.get(Number(index)); }
  get intervalMs() { return median(this._gpuIntervals) || INITIAL_INTERVAL_MS; }
  get freshnessThresholdMs() { return staleAfterMs(this.intervalMs); }
  maxGapMs() { return Math.max(this.intervalMs * 5, 3000); }
}
