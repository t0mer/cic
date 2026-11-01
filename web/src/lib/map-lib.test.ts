import { describe, expect, it } from 'vitest'

import { boundsOf } from './bounds'
import { fanPositions, groupByPixelDistance, placeTooltip, spreadOffsets } from './cluster'
import { makeDevice } from './fixtures'
import { describeLocation, GEOCODE_GRACE_S } from './location'
import { googleMapsUrl } from './maps'
import { escapeHtml, parseUiConfig } from './ui-config'

const pt = (id: string, x: number, y: number) => ({ id, x, y })

describe('groupByPixelDistance', () => {
  it('keeps distant points apart and groups close ones', () => {
    const g = groupByPixelDistance([pt('a', 0, 0), pt('b', 10, 10), pt('c', 200, 200)])
    expect(g.map((x) => x.map((p) => p.id))).toEqual([['a', 'b'], ['c']])
  })
  it('is transitive (chains) and honours the threshold edge', () => {
    const g = groupByPixelDistance([pt('a', 0, 0), pt('b', 20, 0), pt('c', 40, 0)])
    expect(g).toHaveLength(1)
    expect(groupByPixelDistance([pt('a', 0, 0), pt('b', 24, 0)])).toHaveLength(2)
    expect(groupByPixelDistance([pt('a', 0, 0), pt('b', 23.9, 0)])).toHaveLength(1)
  })
  it('handles empty input', () => expect(groupByPixelDistance([])).toEqual([]))
})

describe('spreadOffsets', () => {
  it('leaves lone markers alone', () => {
    expect(spreadOffsets([pt('a', 5, 5)]).get('a')).toEqual([0, 0])
  })
  it('fans identical positions out so every marker is separately reachable', () => {
    const pts = [pt('a', 100, 100), pt('b', 100, 100), pt('c', 100, 100)]
    const off = spreadOffsets(pts)
    const placed = pts.map((p) => {
      const o = off.get(p.id) as [number, number]
      return { x: p.x + o[0], y: p.y + o[1] }
    })
    for (let i = 0; i < placed.length; i++)
      for (let j = i + 1; j < placed.length; j++)
        expect(Math.hypot(placed[i].x - placed[j].x, placed[i].y - placed[j].y)).toBeGreaterThan(24)
  })
  it('fanPositions grows the radius with the count', () => {
    expect(fanPositions(1)).toEqual([[0, 0]])
    const big = fanPositions(20).map(([x, y]) => Math.hypot(x, y))
    expect(Math.min(...big)).toBeGreaterThan(80)
  })
})

describe('placeTooltip', () => {
  const c = { width: 375, height: 600 }
  const tip = { width: 256, height: 230 }
  it('goes above when there is room, below near the top', () => {
    expect(placeTooltip({ x: 187, y: 400 }, c, tip).side).toBe('top')
    expect(placeTooltip({ x: 187, y: 100 }, c, tip).side).toBe('bottom')
  })
  it('uses the measured height: a short tooltip fits above where a tall one does not', () => {
    const a = { x: 187, y: 150 }
    expect(placeTooltip(a, c, { width: 256, height: 100 }).side).toBe('top')
    expect(placeTooltip(a, c, { width: 256, height: 300 }).side).toBe('bottom')
  })
  it('when neither side fits it takes the roomier side and reports the room left', () => {
    const small = { width: 375, height: 300 }
    const p = placeTooltip({ x: 187, y: 200 }, small, { width: 256, height: 400 })
    expect(p.side).toBe('top') // 164 above vs 64 below
    expect(p.maxHeight).toBe(164)
    const q = placeTooltip({ x: 187, y: 80 }, small, { width: 256, height: 400 })
    expect(q.side).toBe('bottom')
    expect(q.maxHeight).toBe(300 - 80 - 28 - 8)
  })
  it('never reports a negative height', () => {
    expect(placeTooltip({ x: 10, y: 5 }, { width: 100, height: 20 }, tip).maxHeight).toBe(0)
  })
  it('shifts horizontally to stay inside the map', () => {
    expect(placeTooltip({ x: 187, y: 400 }, c, tip).shiftX).toBe(0)
    expect(placeTooltip({ x: 10, y: 400 }, c, tip).shiftX).toBeGreaterThan(0)
    expect(placeTooltip({ x: 370, y: 400 }, c, tip).shiftX).toBeLessThan(0)
  })
})

describe('boundsOf', () => {
  it('returns null when empty and the box otherwise', () => {
    expect(boundsOf([])).toBeNull()
    expect(
      boundsOf([
        { lng: 34, lat: 32 },
        { lng: 35, lat: 31 },
      ]),
    ).toEqual([
      [34, 31],
      [35, 32],
    ])
  })
})

describe('googleMapsUrl', () => {
  it('builds the URL from numbers only', () => {
    expect(googleMapsUrl(32.08531, 34.78176)).toBe(
      'https://www.google.com/maps/search/?api=1&query=32.08531,34.78176',
    )
  })
  it('never interpolates non-numeric input', () => {
    const evil = '1&x=<script>' as unknown as number
    expect(googleMapsUrl(evil, 2)).toBe('https://www.google.com/maps')
    expect(googleMapsUrl(NaN, 2)).not.toContain('NaN')
  })
})

describe('parseUiConfig', () => {
  const ok = {
    map: {
      provider: 'openfreemap',
      style_light: 'https://tiles.openfreemap.org/styles/positron',
      style_dark: 'https://tiles.openfreemap.org/styles/dark',
      attribution: 'OpenFreeMap',
    },
    geocode_enabled: true,
    history_enabled: true,
  }
  it('accepts a valid config', () => expect(parseUiConfig(ok)).toEqual(ok))
  it('defaults history_enabled to false', () =>
    expect(parseUiConfig({ ...ok, history_enabled: undefined }).history_enabled).toBe(false))
  it('rejects non-https style URLs', () => {
    expect(() =>
      parseUiConfig({ ...ok, map: { ...ok.map, style_light: 'http://x.test/s' } }),
    ).toThrow(/https/)
    expect(() =>
      parseUiConfig({ ...ok, map: { ...ok.map, style_dark: 'javascript:alert(1)' } }),
    ).toThrow()
    expect(() => parseUiConfig({ ...ok, map: { ...ok.map, style_dark: 'not a url' } })).toThrow()
    expect(() => parseUiConfig({ ...ok, map: { ...ok.map, style_light: '' } })).toThrow()
  })
  it('rejects malformed bodies', () => {
    expect(() => parseUiConfig(null)).toThrow()
    expect(() => parseUiConfig({ geocode_enabled: true })).toThrow()
  })
  it('defaults geocode_enabled to false unless exactly true', () => {
    expect(parseUiConfig({ ...ok, geocode_enabled: 'yes' }).geocode_enabled).toBe(false)
  })
  it('escapes attribution text', () => {
    expect(escapeHtml('<img src=x onerror="a">&')).toBe(
      '&lt;img src=x onerror=&quot;a&quot;&gt;&amp;',
    )
  })
})

describe('describeLocation', () => {
  const NOW = 1_000_000
  it('unknown without coordinates', () => {
    expect(describeLocation(makeDevice({ latitude: null, longitude: null }), true, NOW).kind).toBe(
      'unknown',
    )
  })
  it('prefers the name and keeps coordinates as secondary', () => {
    const v = describeLocation(makeDevice({ location_name: 'Dizengoff St, Tel Aviv' }), true, NOW)
    expect(v).toEqual({
      kind: 'named',
      name: 'Dizengoff St, Tel Aviv',
      coords: '32.0853, 34.7818',
      place: null,
    })
  })
  it('carries the safe place in the named, coords and pending views', () => {
    const safe_place = { id: 1, name: 'Home' }
    expect(
      describeLocation(makeDevice({ safe_place, location_name: 'Dizengoff St' }), true, NOW),
    ).toMatchObject({ kind: 'named', place: 'Home' })
    expect(
      describeLocation(makeDevice({ safe_place, location_time: NOW - 600 }), true, NOW),
    ).toEqual({ kind: 'coords', coords: '32.0853, 34.7818', place: 'Home' })
    expect(
      describeLocation(makeDevice({ safe_place, location_time: NOW - 2 }), true, NOW),
    ).toMatchObject({ kind: 'pending', place: 'Home' })
  })
  it('falls back to coordinates for old fixes, or when geocoding is off', () => {
    expect(describeLocation(makeDevice({ location_time: NOW - 600 }), true, NOW).kind).toBe(
      'coords',
    )
    expect(describeLocation(makeDevice({ location_time: NOW }), false, NOW).kind).toBe('coords')
  })
  it('shows a skeleton only for a fresh unnamed fix with geocoding on', () => {
    expect(describeLocation(makeDevice({ location_time: NOW - 2 }), true, NOW).kind).toBe('pending')
    expect(
      describeLocation(makeDevice({ location_time: NOW - GEOCODE_GRACE_S - 1 }), true, NOW).kind,
    ).toBe('coords')
  })
})
