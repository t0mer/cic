import { describe, expect, it } from 'vitest'

import { buildFeatures, DAY_COLORS, formatDistance, groupDays } from './history-days'
import type { RouteDay } from './types'

const day = (date: string, segs: [number, number, number][][], extra: Partial<RouteDay> = {}) => ({
  date,
  distance_m: 1500,
  point_count: segs.flat().length,
  first: segs[0]?.[0]?.[2] ?? 0,
  last: segs.at(-1)?.at(-1)?.[2] ?? 0,
  segments: segs,
  ...extra,
})

describe('groupDays', () => {
  it('sorts by date and assigns stable colours', () => {
    const rows = groupDays([
      day('2026-10-03', [[[1, 1, 30]]]),
      day('2026-10-01', [[[1, 1, 10]]]),
      day('2026-10-02', [[[1, 1, 20]]]),
    ])
    expect(rows.map((r) => r.date)).toEqual(['2026-10-01', '2026-10-02', '2026-10-03'])
    expect(rows.map((r) => r.color)).toEqual(DAY_COLORS.slice(0, 3))
  })
  it('wraps the palette and drops empty segments', () => {
    const many = Array.from({ length: DAY_COLORS.length + 1 }, (_, i) =>
      day(`2026-10-${String(i + 1).padStart(2, '0')}`, [[], [[1, 1, i]]]),
    )
    const rows = groupDays(many)
    expect(rows[DAY_COLORS.length].color).toBe(DAY_COLORS[0])
    expect(rows[0].segments).toHaveLength(1)
  })
  it('is empty for no days', () => expect(groupDays([])).toEqual([]))
})

describe('formatDistance', () => {
  it('uses metres under a km and km above', () => {
    expect(formatDistance(850)).toBe('850 m')
    expect(formatDistance(1500)).toBe('1.5 km')
  })
})

describe('buildFeatures', () => {
  it('builds lon/lat lines, vertices, start/end and bounds', () => {
    const rows = groupDays([
      day('2026-10-01', [
        [
          [32, 34, 100],
          [32.1, 34.2, 200],
        ],
      ]),
      day('2026-10-02', [
        [
          [33, 35, 300],
          [33.5, 35.5, 400],
        ],
        [[31, 36, 500]], // a single point draws no line but is still a vertex
      ]),
    ])
    const f = buildFeatures(rows)
    expect(f.lines.features).toHaveLength(2)
    expect(f.lines.features[0].geometry.coordinates[0]).toEqual([34, 32])
    expect(f.points.features).toHaveLength(5)
    const kinds = f.ends.features.map((e) => [e.properties?.kind, e.properties?.t])
    expect(kinds).toEqual([
      ['start', 100],
      ['end', 500],
    ])
    expect(f.bounds).toEqual([
      [34, 31],
      [36, 33.5],
    ])
  })
  it('has no bounds when empty', () => {
    expect(buildFeatures([]).bounds).toBeNull()
  })
})
