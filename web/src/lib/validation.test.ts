import { describe, expect, it } from 'vitest'

import {
  colorProblem,
  DEVICE_NAME_MAX,
  deviceNameLength,
  deviceNameProblem,
  isHexColor,
  MIN_PASSWORD,
  newUserPasswordProblem,
  normalizeColor,
  normalizeDeviceName,
  usernameProblem,
} from './validation'

describe('colour', () => {
  it('accepts only #rrggbb', () => {
    expect(isHexColor('#1a73e8')).toBe(true)
    expect(isHexColor('#1A73E8')).toBe(true)
    for (const bad of ['', '#fff', '1a73e8', '#1a73e', '#1a73e8f', '#gggggg', 'red'])
      expect(isHexColor(bad)).toBe(false)
  })
  it('empty means automatic and is valid; junk is not', () => {
    expect(colorProblem('')).toBeNull()
    expect(colorProblem('  ')).toBeNull()
    expect(colorProblem('#abcdef')).toBeNull()
    expect(colorProblem('#abc')).not.toBeNull()
  })
  it('normalises case and trims', () => {
    expect(normalizeColor(' #ABCDEF ')).toBe('#abcdef')
    expect(normalizeColor('nope')).toBe('nope')
  })
})

describe('device name', () => {
  it('counts trimmed characters, not UTF-16 units', () => {
    expect(deviceNameLength('  שלום  ')).toBe(4)
    expect(deviceNameLength('😀'.repeat(80))).toBe(80)
  })
  it('limits to 80', () => {
    expect(deviceNameProblem('a'.repeat(DEVICE_NAME_MAX))).toBeNull()
    expect(deviceNameProblem('a'.repeat(DEVICE_NAME_MAX + 1))).not.toBeNull()
    expect(deviceNameProblem(`  ${'a'.repeat(DEVICE_NAME_MAX)}  `)).toBeNull()
    expect(deviceNameProblem('')).toBeNull()
  })
})

describe('username', () => {
  it('matches ^[A-Za-z0-9._-]{3,64}$', () => {
    for (const ok of ['abc', 'a.b_c-d', 'A'.repeat(64)]) expect(usernameProblem(ok)).toBeNull()
    for (const bad of ['ab', 'a'.repeat(65), 'has space', 'tomer@x', 'שלום', '', 'a\nb'])
      expect(usernameProblem(bad)).not.toBeNull()
  })
})

describe('new user password', () => {
  it('is optional, otherwise at least 12 characters', () => {
    expect(newUserPasswordProblem('')).toBeNull()
    expect(newUserPasswordProblem('x'.repeat(MIN_PASSWORD - 1))).not.toBeNull()
    expect(newUserPasswordProblem('x'.repeat(MIN_PASSWORD))).toBeNull()
    expect(newUserPasswordProblem('😀'.repeat(MIN_PASSWORD))).toBeNull()
  })
})

describe('device name rules', () => {
  it('collapses runs of spaces and trims', () => {
    expect(normalizeDeviceName('  a   b  ')).toBe('a b')
    expect(deviceNameLength('a    b')).toBe(3)
  })
  it('rejects control, format, separator and filler characters', () => {
    for (const bad of [
      'a\tb',
      'a\nb',
      'a\u200Bb',
      'a\u202Eb',
      'a\u2028b',
      'a\u2029b',
      'a\u00A0b',
      'a\u2003b',
      'a\u3000b',
      '\u3164',
      '\u2800',
      'a\u115Fb',
      'a\u1160b',
      'a\uFFA0b',
    ])
      expect(deviceNameProblem(bad), JSON.stringify(bad)).not.toBeNull()
  })
  it('accepts ordinary names including Hebrew, emoji and a single space', () => {
    for (const ok of ['', '   ', 'Pixel 8', 'שלום עולם', 'Tom\u2019s phone', '📱 Dana'])
      expect(deviceNameProblem(ok)).toBeNull()
  })
})
