// Pure ASCII pyramid renderer. No React, no DOM: renderPyramid(opts) -> string.
// Single-hue by design: shading is done with a character ramp, color comes from CSS.

export interface PyramidOptions {
  theta?: number;
  w?: number;
  h?: number;
  axis?: 'x' | 'y' | 'z';
  tilt?: number;
  edges?: boolean;
  ramp?: string;
  edgeChar?: string;
  ambient?: number;
}

const BASE: [number, number, number][] = [
  [0, 1.2, 0],
  [-1, -0.8, -1], [1, -0.8, -1], [1, -0.8, 1], [-1, -0.8, 1],
];
const FACES: [number, number, number][] = [[0, 1, 2], [0, 2, 3], [0, 3, 4], [0, 4, 1]];
const EDGES: [number, number][] = [[0, 1], [0, 2], [0, 3], [0, 4], [1, 2], [2, 3], [3, 4], [4, 1]];
const DIST = 4.2;
const LIGHT = norm([-0.3, 0.6, -0.75]);

const C = BASE.reduce((a, v) => [a[0] + v[0] / 5, a[1] + v[1] / 5, a[2] + v[2] / 5], [0, 0, 0]);
const MODEL = BASE.map((v) => [v[0] - C[0], v[1] - C[1], v[2] - C[2]]) as [number, number, number][];

function norm(v: [number, number, number]): [number, number, number] {
  const r = Math.hypot(v[0], v[1], v[2]) || 1;
  return [v[0] / r, v[1] / r, v[2] / r];
}
function sub(a: [number, number, number], b: [number, number, number]): [number, number, number] {
  return [a[0] - b[0], a[1] - b[1], a[2] - b[2]];
}
function cross(a: [number, number, number], b: [number, number, number]): [number, number, number] {
  return [a[1] * b[2] - a[2] * b[1], a[2] * b[0] - a[0] * b[2], a[0] * b[1] - a[1] * b[0]];
}
function dot(a: [number, number, number], b: [number, number, number]): number {
  return a[0] * b[0] + a[1] * b[1] + a[2] * b[2];
}

function rotate(v: [number, number, number], axis: 'x' | 'y' | 'z', t: number): [number, number, number] {
  const c = Math.cos(t), s = Math.sin(t), [x, y, z] = v;
  if (axis === 'x') return [x, y * c - z * s, y * s + z * c];
  if (axis === 'z') return [x * c - y * s, x * s + y * c, z];
  return [x * c + z * s, y, -x * s + z * c];
}

export function renderPyramid({
  theta = 0, w = 64, h = 32, axis = 'y', tilt = 0.35,
  edges = false, ramp = ' .:-=+*#%@', edgeChar = '#', ambient = 0.18,
} = {} as PyramidOptions): string {
  const f = h * 1.35; const aspect = 2;
  const place = (v: [number, number, number]): [number, number, number] => {
    const r = rotate(rotate(v, axis, theta), 'x', tilt);
    return [r[0], r[1], r[2] + DIST];
  };
  const P = MODEL.map(place);
  const scr = (p: [number, number, number]): [number, number] => [
    w / 2 + (f * aspect * p[0]) / p[2],
    h / 2 - (f * p[1]) / p[2],
  ];
  const S = P.map(scr);
  const buf = new Array(w * h).fill(' ');
  const zb = new Float32Array(w * h);

  for (const [a, b, c] of FACES) {
    let n = norm(cross(sub(P[b], P[a]), sub(P[c], P[a])));
    const ctr: [number, number, number] = [
      (P[a][0] + P[b][0] + P[c][0]) / 3,
      (P[a][1] + P[b][1] + P[c][1]) / 3,
      (P[a][2] + P[b][2] + P[c][2]) / 3,
    ];
    if (dot(n, ctr) > 0) n = [-n[0], -n[1], -n[2]];
    if (dot(n, [-ctr[0], -ctr[1], -ctr[2]]) <= 0) continue;
    const L = ambient + (1 - ambient) * Math.max(0, dot(n, LIGHT));
    const ch = ramp[1 + Math.floor(L * (ramp.length - 2))];
    const [A, B, Cc] = [S[a], S[b], S[c]];
    const area = (B[0] - A[0]) * (Cc[1] - A[1]) - (B[1] - A[1]) * (Cc[0] - A[0]);
    if (Math.abs(area) < 1e-6) continue;
    const x0 = Math.max(0, Math.floor(Math.min(A[0], B[0], Cc[0])));
    const x1 = Math.min(w - 1, Math.ceil(Math.max(A[0], B[0], Cc[0])));
    const y0 = Math.max(0, Math.floor(Math.min(A[1], B[1], Cc[1])));
    const y1 = Math.min(h - 1, Math.ceil(Math.max(A[1], B[1], Cc[1])));
    for (let y = y0; y <= y1; y++) {
      for (let x = x0; x <= x1; x++) {
        const px = x + 0.5, py = y + 0.5;
        const w0 = ((B[0] - px) * (Cc[1] - py) - (B[1] - py) * (Cc[0] - px)) / area;
        const w1 = ((Cc[0] - px) * (A[1] - py) - (Cc[1] - py) * (A[0] - px)) / area;
        const w2 = 1 - w0 - w1;
        if (w0 < 0 || w1 < 0 || w2 < 0) continue;
        const iz = w0 / P[a][2] + w1 / P[b][2] + w2 / P[c][2];
        const i = x + y * w;
        if (iz > zb[i]) { zb[i] = iz; buf[i] = ch; }
      }
    }
  }

  if (edges) {
    for (const [a, b] of EDGES) {
      const steps = Math.ceil(Math.hypot(S[b][0] - S[a][0], S[b][1] - S[a][1]) * 2) + 1;
      for (let k = 0; k <= steps; k++) {
        const t = k / steps;
        const x = Math.round(S[a][0] + (S[b][0] - S[a][0]) * t);
        const y = Math.round(S[a][1] + (S[b][1] - S[a][1]) * t);
        if (x < 0 || x >= w || y < 0 || y >= h) continue;
        const iz = 1 / (P[a][2] + (P[b][2] - P[a][2]) * t);
        const i = x + y * w;
        if (iz >= zb[i] * 0.97) { zb[i] = Math.max(zb[i], iz); buf[i] = edgeChar; }
      }
    }
  }

  const rows: string[] = [];
  for (let y = 0; y < h; y++) {
    rows.push(buf.slice(y * w, (y + 1) * w).join('').replace(/\s+$/, ''));
  }
  return rows.join('\n');
}
