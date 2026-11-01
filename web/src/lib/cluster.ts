export interface PixelPoint {
  id: string
  x: number
  y: number
}

export type Offset = [number, number]

/** Markers closer than this (px) are fanned out. */
export const OVERLAP_PX = 24

/** Groups points that are within `threshold` px of each other (transitively). Order is stable. */
export function groupByPixelDistance(points: PixelPoint[], threshold = OVERLAP_PX): PixelPoint[][] {
  const parent = points.map((_, i) => i)
  const find = (i: number): number => {
    while (parent[i] !== i) {
      parent[i] = parent[parent[i]]
      i = parent[i]
    }
    return i
  }
  for (let i = 0; i < points.length; i++)
    for (let j = i + 1; j < points.length; j++)
      if (Math.hypot(points[i].x - points[j].x, points[i].y - points[j].y) < threshold)
        parent[find(j)] = find(i)
  const groups = new Map<number, PixelPoint[]>()
  points.forEach((p, i) => {
    const r = find(i)
    const g = groups.get(r)
    if (g) g.push(p)
    else groups.set(r, [p])
  })
  return [...groups.values()]
}

/** Positions on a circle around (0,0), starting at 12 o'clock; a single item stays at the centre. */
export function fanPositions(n: number, minRadius = 30, spacing = 28): Offset[] {
  if (n <= 1) return [[0, 0]]
  const r = Math.max(minRadius, (n * spacing) / (2 * Math.PI))
  return Array.from({ length: n }, (_, i) => {
    const a = -Math.PI / 2 + (2 * Math.PI * i) / n
    return [Math.round(Math.cos(a) * r), Math.round(Math.sin(a) * r)] as Offset
  })
}

/**
 * Per-marker pixel offsets that spread every overlapping group around its centroid ("spiderfy").
 * Markers that overlap nothing get [0, 0].
 */
export function spreadOffsets(points: PixelPoint[], threshold = OVERLAP_PX): Map<string, Offset> {
  const out = new Map<string, Offset>()
  for (const g of groupByPixelDistance(points, threshold)) {
    if (g.length === 1) {
      out.set(g[0].id, [0, 0])
      continue
    }
    const cx = g.reduce((s, p) => s + p.x, 0) / g.length
    const cy = g.reduce((s, p) => s + p.y, 0) / g.length
    const fan = fanPositions(g.length)
    g.forEach((p, i) =>
      out.set(p.id, [Math.round(cx + fan[i][0] - p.x), Math.round(cy + fan[i][1] - p.y)]),
    )
  }
  return out
}

/** `next`, but reusing `prev` (and its tuples) wherever the values are equal, so identity means change. */
export function stableOffsets(
  prev: Map<string, Offset>,
  next: Map<string, Offset>,
): Map<string, Offset> {
  let same = prev.size === next.size
  const out = new Map<string, Offset>()
  for (const [k, v] of next) {
    const p = prev.get(k)
    if (p && p[0] === v[0] && p[1] === v[1]) out.set(k, p)
    else {
      out.set(k, v)
      same = false
    }
  }
  return same ? prev : out
}

export interface Placement {
  side: 'top' | 'bottom'
  /** Horizontal shift (px) that keeps the tooltip inside the map. */
  shiftX: number
  /** Tallest the tooltip may be on the chosen side before it has to scroll. */
  maxHeight: number
}

/**
 * Where a tooltip anchored at (x, y) fits inside a container. Prefers the top; falls back to the
 * bottom; when neither side has room for the measured `tip` it takes the roomier side and reports
 * the height left there so the caller can cap and scroll the tooltip.
 */
export function placeTooltip(
  anchor: { x: number; y: number },
  container: { width: number; height: number },
  tip: { width: number; height: number },
  margin = 8,
  gap = 28,
): Placement {
  const roomTop = anchor.y - gap - margin
  const roomBottom = container.height - anchor.y - gap - margin
  const side: Placement['side'] =
    roomTop >= tip.height
      ? 'top'
      : roomBottom >= tip.height
        ? 'bottom'
        : roomTop > roomBottom
          ? 'top'
          : 'bottom'
  const room = side === 'top' ? roomTop : roomBottom
  const w = Math.min(tip.width, container.width - 2 * margin)
  const left = anchor.x - w / 2
  let shiftX = 0
  if (left < margin) shiftX = margin - left
  else if (left + w > container.width - margin) shiftX = container.width - margin - (left + w)
  return { side, shiftX: Math.round(shiftX), maxHeight: Math.max(0, Math.floor(room)) }
}
