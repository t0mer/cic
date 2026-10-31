import { describe, expect, it } from 'vitest'

import {
  hasForbiddenChars,
  isValidE164,
  maskPhone,
  normalizePhone,
  smsParts,
  smsSegments,
} from './sms'

describe('smsSegments GSM-7', () => {
  it('counts empty as zero segments', () => expect(smsSegments('').segments).toBe(0))
  it.each([
    [1, 1],
    [160, 1],
    [161, 2],
    [306, 2],
    [307, 3],
  ])('%i basic chars -> %i segments', (n, seg) => {
    const r = smsSegments('a'.repeat(n))
    expect(r.encoding).toBe('GSM-7')
    expect(r.segments).toBe(seg)
    expect(r.characters).toBe(n)
  })
  it('counts extension characters as two septets', () => {
    const r = smsSegments('{}')
    expect(r.units).toBe(4)
    expect(r.characters).toBe(2)
    expect(smsSegments('€'.repeat(80)).segments).toBe(1)
    expect(smsSegments('€'.repeat(81)).segments).toBe(2)
    for (const c of ['^', '{', '}', '\\', '[', '~', ']', '|', '€'])
      expect(smsSegments(c).units).toBe(2)
  })
  it('accepts accented GSM letters and newline', () =>
    expect(smsSegments('é ü à\nØ').encoding).toBe('GSM-7'))
  it('never lets an extension pair straddle a part boundary', () => {
    const text = 'a'.repeat(152) + '€' + 'b'.repeat(10)
    const parts = smsParts(text)
    expect(parts).toHaveLength(2)
    expect(parts[0]).toBe('a'.repeat(152))
    expect(parts[1].startsWith('€')).toBe(true)
  })
  it('153 per part exactly', () => {
    const parts = smsParts('a'.repeat(306))
    expect(parts.map((p) => p.length)).toEqual([153, 153])
  })
})

describe('smsSegments Unicode', () => {
  it('Hebrew is Unicode', () => {
    const r = smsSegments('שלום עולם')
    expect(r.encoding).toBe('Unicode')
    expect(r.segments).toBe(1)
  })
  it('emoji is Unicode and costs two code units', () => {
    const r = smsSegments('😀')
    expect(r.encoding).toBe('Unicode')
    expect(r.units).toBe(2)
    expect(r.characters).toBe(1)
  })
  it.each([
    [70, 1],
    [71, 2],
    [134, 2],
    [135, 3],
  ])('%i Hebrew chars -> %i segments', (n, seg) =>
    expect(smsSegments('א'.repeat(n)).segments).toBe(seg),
  )
  it('one non-GSM char switches the whole message to Unicode', () => {
    const r = smsSegments('a'.repeat(100) + 'ש')
    expect(r.encoding).toBe('Unicode')
    expect(r.segments).toBe(2)
  })
  it('never splits a surrogate pair', () => {
    const text = 'א'.repeat(66) + '😀' + 'א'.repeat(10)
    const parts = smsParts(text)
    expect(parts).toHaveLength(2)
    expect(parts[0]).toBe('א'.repeat(66))
    expect(parts[1].startsWith('😀')).toBe(true)
    for (const p of parts) expect(p).not.toMatch(/[\ud800-\udbff]$|^[\udc00-\udfff]/)
  })
})

describe('phone', () => {
  it.each(['+972501234567', '+1234567', '+123456789012345'])('accepts %s', (p) =>
    expect(isValidE164(p)).toBe(true),
  )
  it.each([
    '972501234567',
    '+0501234567',
    '+12345',
    '+1234567890123456',
    '+97 250',
    '',
    '+9725a1234',
  ])('rejects %s', (p) => expect(isValidE164(p)).toBe(false))
  it('normalizes pasted spacing', () =>
    expect(normalizePhone(' +972 50-123 4567 ')).toBe('+972501234567'))
  it('masks the middle', () => expect(maskPhone('+972501234567')).toBe('+9725•••••567'))
})

describe('hasForbiddenChars', () => {
  it('allows newline and Hebrew', () => expect(hasForbiddenChars('שלום\nhi')).toBe(false))
  it('rejects control and format characters', () => {
    expect(hasForbiddenChars('a\u0000b')).toBe(true)
    expect(hasForbiddenChars('a‮b')).toBe(true)
    expect(hasForbiddenChars('a\tb')).toBe(true)
  })
})
