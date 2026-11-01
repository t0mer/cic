import { describe, expect, it } from 'vitest'

import {
  MAX_RANGE_DAYS,
  parseLocalInput,
  presetRange,
  resolveRange,
  toLocalInput,
  withRange,
} from './history-range'

const NOW = new Date(2026, 9, 15, 14, 30) // Thu 15 Oct 2026, local time

describe('local datetime helpers', () => {
  it('round-trips a local value', () => {
    expect(toLocalInput(NOW)).toBe('2026-10-15T14:30')
    expect(parseLocalInput('2026-10-15T14:30')?.getTime()).toBe(NOW.getTime())
  })
  it('rejects malformed and impossible values', () => {
    expect(parseLocalInput('')).toBeNull()
    expect(parseLocalInput(null)).toBeNull()
    expect(parseLocalInput('2026-10-15')).toBeNull()
    expect(parseLocalInput('2026-02-31T10:00')).toBeNull()
  })
})

describe('presets', () => {
  it('covers whole local days', () => {
    expect(presetRange('today', NOW)).toEqual({ from: '2026-10-15T00:00', to: '2026-10-16T00:00' })
    expect(presetRange('yesterday', NOW)).toEqual({
      from: '2026-10-14T00:00',
      to: '2026-10-15T00:00',
    })
    expect(presetRange('7d', NOW)).toEqual({ from: '2026-10-09T00:00', to: '2026-10-16T00:00' })
  })
})

describe('resolveRange', () => {
  it('defaults to today with no params', () => {
    const r = resolveRange(new URLSearchParams(), NOW)
    expect(r.error).toBeNull()
    expect(r.preset).toBe('today')
    expect(r.from?.getTime()).toBe(new Date(2026, 9, 15).getTime())
  })
  it('reads the range from the URL and detects a preset', () => {
    const p = new URLSearchParams(presetRange('yesterday', NOW))
    expect(resolveRange(p, NOW).preset).toBe('yesterday')
    const custom = resolveRange(
      new URLSearchParams({ from: '2026-10-01T08:00', to: '2026-10-02T09:15' }),
      NOW,
    )
    expect(custom.preset).toBeNull()
    expect(custom.error).toBeNull()
  })
  it('requires from before to', () => {
    const r = resolveRange(
      new URLSearchParams({ from: '2026-10-02T00:00', to: '2026-10-01T00:00' }),
      NOW,
    )
    expect(r.error).toMatch(/before/)
    expect(r.from).toBeNull()
    expect(
      resolveRange(new URLSearchParams({ from: '2026-10-02T00:00', to: '2026-10-02T00:00' }), NOW)
        .error,
    ).toMatch(/before/)
  })
  it('allows 31 days but not more', () => {
    expect(MAX_RANGE_DAYS).toBe(31)
    const ok = resolveRange(
      new URLSearchParams({ from: '2026-09-01T00:00', to: '2026-10-02T00:00' }),
      NOW,
    )
    expect(ok.error).toBeNull()
    const bad = resolveRange(
      new URLSearchParams({ from: '2026-09-01T00:00', to: '2026-10-02T00:01' }),
      NOW,
    )
    expect(bad.error).toMatch(/31 days/)
  })
  it('flags garbage', () => {
    expect(resolveRange(new URLSearchParams({ from: 'nope' }), NOW).error).toMatch(/valid/)
  })
})

describe('withRange', () => {
  it('sets and clears ends, keeping other params', () => {
    const a = withRange(new URLSearchParams('x=1'), { from: '2026-10-01T00:00' })
    expect(a.get('from')).toBe('2026-10-01T00:00')
    expect(a.get('x')).toBe('1')
    expect(withRange(a, { from: '' }).has('from')).toBe(false)
  })
})
