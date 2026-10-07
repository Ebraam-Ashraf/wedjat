export interface GpuSpec {
  match: (name: string) => boolean;
  label: string;
  sms: number;
  cuda: number;
  tensor: number;
  rt: number;
  gpcs: number[];
}

export const SPECS: GpuSpec[] = [
  { match: (n) => /3050\s*ti/i.test(n) && /(laptop|mobile)/i.test(n),
    label: 'RTX 3050 Ti Laptop', sms: 20, cuda: 2560, tensor: 80, rt: 20, gpcs: [5, 5] },
  { match: (n) => /3050/i.test(n) && /(laptop|mobile)/i.test(n),
    label: 'RTX 3050 Laptop', sms: 16, cuda: 2048, tensor: 64, rt: 16, gpcs: [4, 4] },
  { match: (n) => /3050/i.test(n) && /6\s?gb/i.test(n),
    label: 'RTX 3050 6GB', sms: 18, cuda: 2304, tensor: 72, rt: 18, gpcs: [5, 4] },
  { match: (n) => /3050/i.test(n) && /8\s?gb/i.test(n),
    label: 'RTX 3050 8GB', sms: 20, cuda: 2560, tensor: 80, rt: 20, gpcs: [5, 5] },
];

export function findSpec(name: string = '', vramBytes: number | null = null): GpuSpec | null {
  const byName = SPECS.find((spec) => spec.match(name));
  if (byName) return byName;
  if (!/3050/i.test(name)) return null;
  const vramGiB = vramBytes != null ? Number(vramBytes) / 1_073_741_824 : NaN;
  if (Number.isFinite(vramGiB) && vramGiB >= 5.5 && vramGiB <= 6.5)
    return SPECS.find((s) => s.label === 'RTX 3050 6GB') ?? null;
  if (Number.isFinite(vramGiB) && vramGiB >= 7.5 && vramGiB <= 8.5)
    return SPECS.find((s) => s.label === 'RTX 3050 8GB') ?? null;
  return null;
}
