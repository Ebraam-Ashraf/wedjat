// Central type definitions for Wedjat web UI.

export interface GpuSample {
  index: number;
  uuid: string;
  util: number | null;
  mem: number | null;
  mem_util: number | null;
  temp: number | null;
  power: number | null;
  power_limit: number | null;
  clock: number | null;
  mem_clock: number | null;
  throttle: number | null;
  ecc: number | null;
  t?: number;
  lastSampleAt?: number;
  intervalMs?: number;
  sequence?: number;
  sampleTimestamp?: number;
  cadenceMeasured?: boolean;
  freshnessThresholdMs?: number;
  maxGapMs?: number;
}

export interface GpuInfo {
  uuid: string;
  index: number;
  name?: string;
  vram_total_bytes?: number;
  driver_version?: string;
  pci_bus_id?: string;
  first_seen?: string;
  last_seen?: string;
  id?: number;
}

export interface LiveProcess {
  PID: number | string;
  GPUUUID?: string;
  VRAMValid?: boolean;
  VRAMBytes?: number;
  Tgid?: number;
  ApiID?: number;
  Ordinal?: number;
  Count?: number;
  Bytes?: number;
  LatencySumNs?: number;
  LatencyMaxNs?: number;
  AllocBytes?: number;
  FreeBytes?: number;
  Errors?: number;
  UvmFaults?: number;
  UvmEvicts?: number;
}

export interface ProcessRow {
  pid?: number | string;
  tgid?: number;
  gpu_uuid?: string;
  command?: string;
  last_vram_bytes?: number;
  peak_vram_bytes?: number;
  first_seen?: string;
  running?: boolean;
  gpu?: string;
}

export interface AggregateRow {
  tgid?: number;
  gpu_name?: string;
  time?: string;
  launches?: number;
  memcpy_calls?: number;
  memcpy_bytes?: number;
  alloc_calls?: number;
  alloc_bytes?: number;
  free_bytes?: number;
  sync_calls?: number;
  sync_us_sum?: number;
  sync_us_max?: number;
  ioctl_calls?: number;
  uvm_faults?: number;
  uvm_evicts?: number;
  errors?: number;
}

export interface Incident {
  incident_id?: string;
  type?: string;
  last_ts?: string;
  summary?: string;
  detail?: string;
  gpu_name?: string;
  occurrences?: number;
  last_time?: string;
  first_time?: string;
  command?: string;
  tgid?: number;
}

export interface LiveEvent {
  id: string;
  type: string;
  data?: Record<string, unknown>;
  ts: number;
}

export interface StatusResponse {
  connected?: boolean;
  heartbeat_age?: number;
  warning?: string;
}

export interface ApiDataResult<T> {
  data: T | null;
  loading: boolean;
  error: string;
  updatedAt: Date | null;
  refresh: () => Promise<void>;
}

export interface TelemetryState {
  latest: GpuSample[];
  procs: LiveProcess[];
  aggs: LiveProcess[];
  events: LiveEvent[];
  socketConnected: boolean;
  connected: boolean;
  synthetic: boolean;
  lastMessageAt: number;
  lastGpuAt: number;
  lastGpuSampleTimestampMs: number;
  gpuMessageSequence: number;
  stats: {
    messages: number;
    rate: number;
    avgInterval: number;
    minInterval: number;
    maxInterval: number;
    lastReceiveGapMs: number;
    lastSampleGapMs: number;
    intervals: number[];
  };
}

export interface TelemetryRecord {
  index: number;
  uuid: string;
  ring: GpuSample[];
  latest: unknown | null;
  intervals: number[];
  intervalMs: number;
  lastReceivedAt?: number;
  lastSampleAt?: number;
  sequence?: number;
  sampleTimestamp?: number;
  cadenceMeasured?: boolean;
  freshnessThresholdMs?: number;
  maxGapMs?: number;
}

export interface ChartSample {
  t: number;
  util?: number | null;
  util_avg?: number | null;
  util_max?: number | null;
  temp?: number | null;
  temp_max_c?: number | null;
  mem?: number | null;
  vram_used_max_bytes?: number | null;
  power?: number | null;
  power_mw_sum?: number | null;
  mem_clock?: number | null;
}

export interface HistoryRow {
  id: string;
  ts: number;
  time?: string;
  gpu_uuid: string;
  gpu_name?: string;
  util_gpu: number | null;
  util_gpu_avg?: number | null;
  util_gpu_max?: number | null;
  mem_util: number | null;
  temp: number | null;
  temp_max_c?: number | null;
  power: number | null;
  vram_used: number | null;
  vram_used_max_bytes?: number | null;
  power_mw_sum?: number | null;
  n?: number;
}

export interface TelemetryStoreLike {
  latest: GpuSample[];
  procs: LiveProcess[];
  aggs: LiveProcess[];
  events: LiveEvent[];
  socketConnected: boolean;
  connected: boolean;
  synthetic: boolean;
  lastMessageAt: number;
  lastGpuAt: number;
  lastGpuSampleTimestampMs: number;
  gpuMessageSequence: number;
  intervalMs: number;
  freshnessThresholdMs: number;
  stats: TelemetryState['stats'];
  gpuOrder: number[];
  gpu(index: number): TelemetryRecord | undefined;
  maxGapMs(): number;
  subscribe(slice: string, fn: () => void): () => void;
  ingest(message: unknown): void;
  connect(): () => void;
  disconnect(): void;
}
