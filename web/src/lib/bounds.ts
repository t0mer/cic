export type LngLatBox = [[number, number], [number, number]]

/** Bounding box [[west, south], [east, north]] of lng/lat pairs, or null when empty. */
export function boundsOf(points: { lng: number; lat: number }[]): LngLatBox | null {
  if (points.length === 0) return null
  let w = Infinity
  let s = Infinity
  let e = -Infinity
  let n = -Infinity
  for (const p of points) {
    w = Math.min(w, p.lng)
    e = Math.max(e, p.lng)
    s = Math.min(s, p.lat)
    n = Math.max(n, p.lat)
  }
  return [
    [w, s],
    [e, n],
  ]
}
