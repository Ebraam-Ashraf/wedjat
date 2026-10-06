// Build shape-preserving cubic Bezier segments through an x-ordered series.
// The monotone tangents smooth corners without overshooting measured values.
export function monotoneBezierSegments(points) {
  const knots = [];
  for (const point of points) {
    if (!Number.isFinite(point?.x) || !Number.isFinite(point?.y)) continue;
    const previous = knots[knots.length - 1];
    if (previous && point.x < previous.x) continue;
    if (previous && point.x === previous.x) {
      previous.y = point.y;
      continue;
    }
    knots.push({ x: point.x, y: point.y });
  }

  if (knots.length < 2) return [];

  const count = knots.length;
  const widths = new Array(count - 1);
  const slopes = new Array(count - 1);
  for (let i = 0; i < count - 1; i++) {
    widths[i] = knots[i + 1].x - knots[i].x;
    slopes[i] = (knots[i + 1].y - knots[i].y) / widths[i];
  }

  const tangent = new Array(count);
  tangent[0] = slopes[0];
  tangent[count - 1] = slopes[count - 2];
  for (let i = 1; i < count - 1; i++) {
    const before = slopes[i - 1];
    const after = slopes[i];
    if (before === 0 || after === 0 || Math.sign(before) !== Math.sign(after)) {
      tangent[i] = 0;
      continue;
    }
    const beforeWidth = widths[i - 1];
    const afterWidth = widths[i];
    const w1 = 2 * afterWidth + beforeWidth;
    const w2 = afterWidth + 2 * beforeWidth;
    tangent[i] = (w1 + w2) / (w1 / before + w2 / after);
  }

  // Limit endpoint and segment tangents to preserve monotonicity.
  if (count > 2) {
    tangent[0] = endpointTangent(widths[0], widths[1], slopes[0], slopes[1]);
    tangent[count - 1] = endpointTangent(
      widths[count - 2], widths[count - 3], slopes[count - 2], slopes[count - 3],
    );
  }
  for (let i = 0; i < slopes.length; i++) {
    const slope = slopes[i];
    if (slope === 0) {
      tangent[i] = 0;
      tangent[i + 1] = 0;
      continue;
    }
    let alpha = tangent[i] / slope;
    let beta = tangent[i + 1] / slope;
    if (alpha < 0) tangent[i] = alpha = 0;
    if (beta < 0) tangent[i + 1] = beta = 0;
    const magnitude = alpha * alpha + beta * beta;
    if (magnitude > 9) {
      const limit = 3 / Math.sqrt(magnitude);
      tangent[i] = limit * alpha * slope;
      tangent[i + 1] = limit * beta * slope;
    }
  }

  return slopes.map((_, i) => {
    const start = knots[i];
    const end = knots[i + 1];
    const third = widths[i] / 3;
    return {
      start,
      control1: { x: start.x + third, y: start.y + tangent[i] * third },
      control2: { x: end.x - third, y: end.y - tangent[i + 1] * third },
      end,
    };
  });
}

function endpointTangent(firstWidth, nextWidth, firstSlope, nextSlope) {
  let value = ((2 * firstWidth + nextWidth) * firstSlope - firstWidth * nextSlope) /
    (firstWidth + nextWidth);
  if (Math.sign(value) !== Math.sign(firstSlope)) return 0;
  if (Math.sign(firstSlope) !== Math.sign(nextSlope) && Math.abs(value) > Math.abs(3 * firstSlope)) {
    value = 3 * firstSlope;
  }
  return value;
}
