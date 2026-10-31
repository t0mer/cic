import { describe, expect, it } from 'vitest'

import { colorForKey, readableText } from './color'
import { filterDevices, sortDevices } from './device-list'
import { makeDevice } from './fixtures'
import { firstGrapheme } from './grapheme'
import { formatCoords, googleMapsUrl } from './maps'
import { relativeTime } from './time'

describe('firstGrapheme', () => {
  it('uppercases latin', () => expect(firstGrapheme('alice')).toBe('A'))
  it('handles Hebrew', () => expect(firstGrapheme('תומר')).toBe('ת'))
  it('keeps a whole emoji', () => expect(firstGrapheme('👨‍👩‍👧 Family')).toBe('👨‍👩‍👧'))
  it('keeps combining marks', () => expect(firstGrapheme('école')).toBe('É'))
  it('falls back for empty', () => expect(firstGrapheme('  ')).toBe('?'))
})

describe('colorForKey', () => {
  it('is deterministic', () => expect(colorForKey('a/b')).toBe(colorForKey('a/b')))
  it('differs between keys', () => expect(colorForKey('a/b')).not.toBe(colorForKey('a/c')))
  it('prefers a valid explicit color', () => expect(colorForKey('a/b', '#ff0000')).toBe('#ff0000'))
  it('ignores an invalid explicit color', () =>
    expect(colorForKey('a/b', 'red;x')).toMatch(/^hsl\(/))
})

describe('relativeTime', () => {
  const now = 1_800_000_000
  it.each([
    [0, 'never'],
    [now - 10, 'just now'],
    [now - 60, '1 min ago'],
    [now - 600, '10 min ago'],
    [now - 3600, '1 hour ago'],
    [now - 3 * 3600, '3 hours ago'],
    [now - 86400, '1 day ago'],
    [now - 5 * 86400, '5 days ago'],
    [now - 90 * 86400, '3 months ago'],
    [now + 100, 'just now'],
  ])('%s', (ts, out) => expect(relativeTime(ts, now)).toBe(out))
  it('accepts milliseconds', () =>
    expect(relativeTime(now * 1000 - 600_000, now)).toBe('10 min ago'))
})

describe('maps', () => {
  it('builds the url', () =>
    expect(googleMapsUrl(32.0853, 34.7818)).toBe(
      'https://www.google.com/maps/search/?api=1&query=32.0853,34.7818',
    ))
  it('rounds to 4 decimals', () =>
    expect(formatCoords(32.08531, 34.78176)).toBe('32.0853, 34.7818'))
})

describe('filter and sort', () => {
  const a = makeDevice({
    key: 'p/a',
    full_name: 'Alice',
    online: true,
    battery_level: 50,
    last_seen: 100,
  })
  const b = makeDevice({
    key: 'p/b',
    full_name: 'bob',
    online: false,
    battery_level: null,
    last_seen: 0,
  })
  const c = makeDevice({
    key: 'p/c',
    full_name: 'תומר',
    online: false,
    battery_level: 10,
    last_seen: 200,
  })
  const all = [a, b, c]

  it('searches case-insensitively', () => expect(filterDevices(all, 'ALI', 'all')).toEqual([a]))
  it('searches Hebrew', () => expect(filterDevices(all, 'תו', 'all')).toEqual([c]))
  it('filters by status', () => {
    expect(filterDevices(all, '', 'online')).toEqual([a])
    expect(filterDevices(all, '', 'offline')).toEqual([b, c])
  })
  it('sorts by name', () =>
    expect(sortDevices([c, b, a], 'name').map((d) => d.key)).toEqual(['p/a', 'p/b', 'p/c']))
  it('sorts last seen newest first, never-seen last', () =>
    expect(sortDevices(all, 'last_seen').map((d) => d.key)).toEqual(['p/c', 'p/a', 'p/b']))
  it('sorts battery lowest first, unknown last', () =>
    expect(sortDevices(all, 'battery').map((d) => d.key)).toEqual(['p/c', 'p/a', 'p/b']))
  it('does not mutate input', () => {
    const copy = [c, a]
    sortDevices(copy, 'name')
    expect(copy).toEqual([c, a])
  })
})

describe('readableText', () => {
  it.each([
    ['#ff0000', '#000000'],
    ['#00ff00', '#000000'],
    ['#4b0082', '#ffffff'],
    ['#ffffff', '#000000'],
    ['#000000', '#ffffff'],
    ['#fff', '#000000'],
    ['hsl(200 55% 32%)', '#ffffff'],
    ['not a color', '#ffffff'],
  ])('%s -> %s', (bg, fg) => expect(readableText(bg)).toBe(fg))
})

describe('hidden devices', () => {
  it('filterDevices drops hidden devices', () => {
    const list = [makeDevice({ key: 'a/1' }), makeDevice({ key: 'a/2', hidden: true })]
    expect(filterDevices(list, '', 'all').map((d) => d.key)).toEqual(['a/1'])
  })
})
