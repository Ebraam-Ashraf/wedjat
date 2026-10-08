import { formatBytes } from './api';
import type { GpuInfo, GpuSample } from './types';

export const API_NAMES: Record<number, string> = {
  1: 'Context set', 2: 'Context create', 3: 'Context destroy',
  4: 'Kernel launch', 5: 'Allocation', 6: 'Free', 7: 'Memcpy',
  8: 'Sync', 9: 'UVM fault', 10: 'UVM migrate', 11: 'UVM evict',
  12: 'Driver ioctl', 13: 'Memory map', 14: 'SM block start',
  15: 'SM block end', 16: 'Process exec', 17: 'Process exit',
  18: 'UVM ioctl', 19: 'Context pop',
};

export const THROTTLE_REASONS: Array<[number, string]> = [
  [0x002, 'Application clocks'], [0x004, 'Software power cap'],
  [0x008, 'Hardware slowdown'], [0x010, 'Sync boost'], [0x020, 'Software thermal slowdown'],
  [0x040, 'Hardware thermal slowdown'], [0x080, 'Hardware power brake'], [0x100, 'Display clocks'],
  [0x200, 'Board limit'], [0x400, 'Reliability policy'],
];

export function throttleNames(mask: number | string): string[] {
  const value = Number(mask) || 0;
  const knownMask = THROTTLE_REASONS.reduce((all, [bit]) => all | bit, 0);
  const names = THROTTLE_REASONS.filter(([bit]) => (value & bit) !== 0).map(([, name]) => name);
  const unknown = value & ~knownMask;
  if (unknown) names.push(`Other reason 0x${unknown.toString(16)}`);
  return names;
}

export function gpuMetaFor(sample: GpuSample | null | undefined, gpus: GpuInfo[]): GpuInfo | null {
  return gpus.find((gpu) => gpu.uuid === sample?.uuid || Number(gpu.index) === Number(sample?.index)) || null;
}

export { formatBytes };
