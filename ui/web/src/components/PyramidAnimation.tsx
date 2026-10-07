import { useEffect, useRef, useState } from 'react';

const W = 80;
const H = 40;
const SCALE = 2;
const DIST = 4.5;
const DU = 0.015;
const DV = 0.015;
const FACE_SYMBOLS = ['@', '#', '$', '*'];
const FACE_COLORS = ['#e53935', '#43a047', '#fbc02d', '#1e88e5'];

type Vec3 = [number, number, number];

const V: Vec3[] = [
  [0, SCALE, 0],
  [-SCALE, -SCALE, -SCALE],
  [SCALE, -SCALE, -SCALE],
  [SCALE, -SCALE, SCALE],
  [-SCALE, -SCALE, SCALE],
];

const F: [number, number, number][] = [
  [0, 1, 2],
  [0, 2, 3],
  [0, 3, 4],
  [0, 4, 1],
];

const EDGES: [number, number][] = [
  [0, 1], [0, 2], [0, 3], [0, 4],
  [1, 2], [2, 3], [3, 4], [4, 1],
];

function sub(a: Vec3, b: Vec3): Vec3 {
  return [a[0] - b[0], a[1] - b[1], a[2] - b[2]];
}

function cross(a: Vec3, b: Vec3): Vec3 {
  return [
    a[1] * b[2] - a[2] * b[1],
    a[2] * b[0] - a[0] * b[2],
    a[0] * b[1] - a[1] * b[0],
  ];
}

function normalize(v: Vec3): Vec3 {
  const length = Math.sqrt(v[0] * v[0] + v[1] * v[1] + v[2] * v[2]);
  if (length === 0) return [0, 0, 0];
  return [v[0] / length, v[1] / length, v[2] / length];
}

function rotate(point: Vec3, theta: number, axis: 'x' | 'y' | 'z'): Vec3 {
  const [x, y, z] = point;
  const c = Math.cos(theta);
  const s = Math.sin(theta);

  switch (axis) {
    case 'x':
      return [x, y * c - z * s, y * s + z * c];
    case 'z':
      return [x * c - y * s, x * s + y * c, z];
    case 'y':
    default:
      return [x * c + z * s, y, -x * s + z * c];
  }
}

function project(
  point: Vec3,
  offset: number,
  w: number = W,
  h: number = H,
): { x: number; y: number; depth: number } | null {
  const [x, y, z] = point;
  const depth = z + offset;
  if (depth <= 0) return null;
  const invZ = 1 / depth;

   const X_SCALE = 36;
  const Y_SCALE = 18;

  const screenX = Math.floor(w / 2 + X_SCALE * x * invZ);
  const screenY = Math.floor(h / 2 - Y_SCALE * y * invZ - 4);

  if (screenX < 0 || screenX >= w || screenY < 0 || screenY >= h) {
    return null;
  }
  return { x: screenX, y: screenY, depth: invZ };
}

interface PyramidAnimationProps {
  wireframe?: boolean;
  color?: boolean;
  speed?: number;
  axis?: 'x' | 'y' | 'z';
  edges?: boolean;
  w?: number;
  h?: number;
  className?: string;
}

function renderPyramidFrame({
  theta,
  axis,
  wireframe,
  color,
  edges,
  w = W,
  h = H,
}: {
  theta: number;
  axis: 'x' | 'y' | 'z';
  wireframe: boolean;
  color: boolean;
  edges: boolean;
  w?: number;
  h?: number;
}): string {
   const localW = w;
  const localH = h;
  const faceBuffer = new Int16Array(localW * localH);
  const depthBuffer = new Float32Array(localW * localH);
  const brightnessBuffer = new Float32Array(localW * localH);
  faceBuffer.fill(-1);

  const normals = F.map((face) => {
    const a = V[face[0]];
    const b = V[face[1]];
    const c = V[face[2]];
    return normalize(cross(sub(b, a), sub(c, a)));
  });

  const light = normalize([0, 1, -1]);

  const centroid: Vec3 = [
    V.reduce((sum, p) => sum + p[0], 0) / V.length,
    V.reduce((sum, p) => sum + p[1], 0) / V.length,
    V.reduce((sum, p) => sum + p[2], 0) / V.length,
  ];
  const rotatedCentroid = rotate(centroid, theta, axis);
  const offset = DIST - rotatedCentroid[2];

  const projectLocal = (point: Vec3): { x: number; y: number; depth: number } | null =>
    project(point, offset, localW, localH);

  if (!wireframe) {
    for (let f = 0; f < F.length; f++) {
      const [ia, ib, ic] = F[f];
      const A = V[ia];
      const B = V[ib];
      const C = V[ic];

      for (let u = 0; u <= 1; u += DU) {
        for (let v = 0; u + v <= 1; v += DV) {
          const w = 1 - u - v;
          const point: Vec3 = [
            w * A[0] + u * B[0] + v * C[0],
            w * A[1] + u * B[1] + v * C[1],
            w * A[2] + u * B[2] + v * C[2],
          ];

          const rotated = rotate(point, theta, axis);
          const screen = projectLocal(rotated);
          if (!screen) continue;

          const index = screen.y * localW + screen.x;
          if (screen.depth <= depthBuffer[index]) continue;

          depthBuffer[index] = screen.depth;

          const normal = rotate(normals[f], theta, axis);
          let brightness = normal[0] * light[0] + normal[1] * light[1] + normal[2] * light[2];
          brightness = Math.max(0, brightness);

          faceBuffer[index] = f;
          brightnessBuffer[index] = brightness;
        }
      }
    }
  }

  if (edges) {
    for (const [a, b] of EDGES) {
      const A = V[a];
      const B = V[b];
      for (let t = 0; t <= 1; t += 0.002) {
        const point: Vec3 = [
          A[0] + (B[0] - A[0]) * t,
          A[1] + (B[1] - A[1]) * t,
          A[2] + (B[2] - A[2]) * t,
        ];
        const rotated = rotate(point, theta, axis);
        const screen = projectLocal(rotated);
        if (!screen) continue;

        const index = screen.y * localW + screen.x;
        if (screen.depth > depthBuffer[index]) {
          depthBuffer[index] = screen.depth + 0.000001;
          faceBuffer[index] = -2;
        }
      }
    }
  }

  let html = '';
  for (let y = 0; y < localH; y++) {
    for (let x = 0; x < localW; x++) {
      const index = y * localW + x;
      const face = faceBuffer[index];

      if (face === -2) {
        html += '<span style="color:#fff;font-weight:bold">+</span>';
        continue;
      }
      if (face < 0) {
        html += ' ';
        continue;
      }

      const brightness = brightnessBuffer[index];
      const baseColor = color ? FACE_COLORS[face] : '#ffffff';
      const opacity = 0.45 + brightness * 0.55;

      html += `<span style="color:${baseColor};opacity:${opacity}">${FACE_SYMBOLS[face]}</span>`;
    }
    html += '\n';
  }
  return html;
}

export default function PyramidAnimation({
  wireframe = false,
  color = true,
  speed = 0.03,
  axis = 'y',
  edges = true,
  w: propW,
  h: propH,
  className = '',
}: PyramidAnimationProps) {
  const [frame, setFrame] = useState('');
  const thetaRef = useRef(0);
  const animationRef = useRef<number | null>(null);
  const lastFrameRef = useRef(0);

  useEffect(() => {
    const animate = (timestamp: number) => {
      if (timestamp - lastFrameRef.current >= 30) {
        thetaRef.current += speed;
        const html = renderPyramidFrame({
          theta: thetaRef.current,
          axis,
          wireframe,
          color,
          edges,
          w: propW,
          h: propH,
        });
        setFrame(html);
        lastFrameRef.current = timestamp;
      }
      animationRef.current = requestAnimationFrame(animate);
    };

    animationRef.current = requestAnimationFrame(animate);
    return () => {
      if (animationRef.current !== null) {
        cancelAnimationFrame(animationRef.current);
      }
    };
  }, [speed, axis, wireframe, color, edges]);

  return (
    <pre
      className={`font-mono text-xs leading-none whitespace-pre select-none ${className}`}
      style={{
        fontFamily: 'var(--font-mono), ui-monospace, SFMono-Regular, Menlo, Consolas, monospace',
        fontSize: 'var(--text-xs, 9px)',
        lineHeight: '1.15',
        whiteSpace: 'pre',
        userSelect: 'none',
        fontVariantLigatures: 'none',
      }}
      dangerouslySetInnerHTML={{ __html: frame }}
      aria-hidden="true"
    />
  );
}
