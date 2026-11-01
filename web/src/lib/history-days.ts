import type { Feature, FeatureCollection, LineString, Point } from 'geojson'

import type { RouteDay } from './types'

/** Per-day line colours: Okabe-Ito based, readable on the light and dark map styles. */
export const DAY_COLORS = [
  '#0072b2',
  '#d55e00',
  '#009e73',
  '#cc79a7',
  '#e69f00',
  '#56b4e9',
  '#8a2be2',
  '#b22222',
] as const

export interface DayRow {
  date: string
  color: string
  distanceM: number
  pointCount: number
  first: number
  last: number
  segments: RouteDay['segments']
}

/** Orders days by date and gives each a stable colour from its position in the response. */
export function groupDays(days: RouteDay[]): DayRow[] {
  return [...days]
    .sort((a, b) => a.date.localeCompare(b.date))
    .map((d, i) => ({
      date: d.date,
      color: DAY_COLORS[i % DAY_COLORS.length],
      distanceM: d.distance_m,
      pointCount: d.point_count,
      first: d.first,
      last: d.last,
      segments: d.segments.filter((s) => s.length > 0),
    }))
}

export function formatDistance(m: number): string {
  if (m < 1000) return `${Math.round(m)} m`
  return `${(m / 1000).toFixed(1)} km`
}

/** "Mon, Oct 5" for a YYYY-MM-DD string (shown as a calendar date, no zone shift). */
export function formatDayLabel(date: string): string {
  const [y, m, d] = date.split('-').map(Number)
  if (!y || !m || !d) return date
  return new Date(Date.UTC(y, m - 1, d)).toLocaleDateString(undefined, {
    weekday: 'short',
    month: 'short',
    day: 'numeric',
    timeZone: 'UTC',
  })
}

export const formatClock = (t: number): string =>
  new Date(t * 1000).toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' })

export const formatDateTime = (t: number): string =>
  new Date(t * 1000).toLocaleString(undefined, {
    month: 'short',
    day: 'numeric',
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
  })

export interface RouteFeatures {
  lines: FeatureCollection<LineString>
  points: FeatureCollection<Point>
  ends: FeatureCollection<Point>
  bounds: [[number, number], [number, number]] | null
}

/** GeoJSON for the visible days: one line per segment, every vertex, and the start/end of the trip. */
export function buildFeatures(days: DayRow[]): RouteFeatures {
  const lines: Feature<LineString>[] = []
  const points: Feature<Point>[] = []
  let first: { lon: number; lat: number; t: number } | null = null
  let last: { lon: number; lat: number; t: number } | null = null
  let w = Infinity
  let s = Infinity
  let e = -Infinity
  let n = -Infinity
  for (const d of days) {
    for (const seg of d.segments) {
      for (const [lat, lon, t] of seg) {
        points.push({
          type: 'Feature',
          properties: { t, color: d.color },
          geometry: { type: 'Point', coordinates: [lon, lat] },
        })
        w = Math.min(w, lon)
        e = Math.max(e, lon)
        s = Math.min(s, lat)
        n = Math.max(n, lat)
        if (!first || t < first.t) first = { lon, lat, t }
        if (!last || t >= last.t) last = { lon, lat, t }
      }
      if (seg.length >= 2)
        lines.push({
          type: 'Feature',
          properties: { color: d.color, date: d.date },
          geometry: { type: 'LineString', coordinates: seg.map(([lat, lon]) => [lon, lat]) },
        })
    }
  }
  const end = (p: typeof first, kind: 'start' | 'end'): Feature<Point>[] =>
    p
      ? [
          {
            type: 'Feature',
            properties: { kind, t: p.t },
            geometry: { type: 'Point', coordinates: [p.lon, p.lat] },
          },
        ]
      : []
  return {
    lines: { type: 'FeatureCollection', features: lines },
    points: { type: 'FeatureCollection', features: points },
    ends: { type: 'FeatureCollection', features: [...end(first, 'start'), ...end(last, 'end')] },
    bounds: points.length
      ? [
          [w, s],
          [e, n],
        ]
      : null,
  }
}
