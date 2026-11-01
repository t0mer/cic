// @vitest-environment jsdom
import { act, renderHook } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { CLOSE_DELAY_MS, OPEN_DELAY_MS, useHoverIntent } from './useHoverIntent'

beforeEach(() => vi.useFakeTimers())
afterEach(() => vi.useRealTimers())
const adv = (ms: number) => act(() => void vi.advanceTimersByTime(ms))

describe('useHoverIntent', () => {
  it('opens ~150 ms after entering', () => {
    const { result } = renderHook(() => useHoverIntent())
    act(() => result.current.enter('a'))
    adv(OPEN_DELAY_MS - 1)
    expect(result.current.activeKey).toBeNull()
    adv(1)
    expect(result.current.activeKey).toBe('a')
  })

  it('does not open when the pointer leaves before the open delay', () => {
    const { result } = renderHook(() => useHoverIntent())
    act(() => result.current.enter('a'))
    adv(100)
    act(() => result.current.leave())
    adv(1000)
    expect(result.current.activeKey).toBeNull()
  })

  it('closes ~300 ms after leaving, and stays open when moving marker -> tooltip', () => {
    const { result } = renderHook(() => useHoverIntent())
    act(() => result.current.enter('a'))
    adv(OPEN_DELAY_MS)
    act(() => result.current.leave()) // left the marker
    adv(100)
    act(() => result.current.enter('a')) // reached the tooltip
    adv(1000)
    expect(result.current.activeKey).toBe('a')
    act(() => result.current.leave())
    adv(CLOSE_DELAY_MS - 1)
    expect(result.current.activeKey).toBe('a')
    adv(1)
    expect(result.current.activeKey).toBeNull()
  })

  it('switches to another marker without being closed by the first one', () => {
    const { result } = renderHook(() => useHoverIntent())
    act(() => result.current.enter('a'))
    adv(OPEN_DELAY_MS)
    act(() => result.current.leave())
    act(() => result.current.enter('b'))
    adv(OPEN_DELAY_MS)
    adv(1000)
    expect(result.current.activeKey).toBe('b')
  })

  it('show/hide are immediate and hide cancels pending timers', () => {
    const { result } = renderHook(() => useHoverIntent())
    act(() => result.current.show('a'))
    expect(result.current.activeKey).toBe('a')
    act(() => result.current.hide())
    expect(result.current.activeKey).toBeNull()
    act(() => result.current.enter('b'))
    act(() => result.current.hide())
    adv(1000)
    expect(result.current.activeKey).toBeNull()
  })

  it('stays open while held, and closes after the hold is released', () => {
    const { result } = renderHook(() => useHoverIntent())
    act(() => result.current.show('a'))
    act(() => result.current.setHold(true))
    act(() => result.current.leave())
    adv(2000)
    expect(result.current.activeKey).toBe('a')
    act(() => result.current.setHold(false))
    adv(CLOSE_DELAY_MS)
    expect(result.current.activeKey).toBeNull()
  })

  it('drops a hold when the tooltip closes, so the next one is not stuck open', () => {
    const { result } = renderHook(() => useHoverIntent())
    act(() => result.current.show('a'))
    act(() => result.current.setHold(true))
    act(() => result.current.hide())
    act(() => result.current.show('b'))
    act(() => result.current.leave())
    adv(CLOSE_DELAY_MS)
    expect(result.current.activeKey).toBeNull()
  })

  it('drops a hold when switching to another marker', () => {
    const { result } = renderHook(() => useHoverIntent())
    act(() => result.current.show('a'))
    act(() => result.current.setHold(true))
    act(() => result.current.show('b'))
    act(() => result.current.leave())
    adv(CLOSE_DELAY_MS)
    expect(result.current.activeKey).toBeNull()
  })

  it('release closes only the open marker and drops its hold', () => {
    const { result } = renderHook(() => useHoverIntent())
    act(() => result.current.show('a'))
    act(() => result.current.setHold(true))
    act(() => result.current.release('zzz'))
    expect(result.current.activeKey).toBe('a')
    act(() => result.current.release('a'))
    expect(result.current.activeKey).toBeNull()
    act(() => result.current.show('a'))
    act(() => result.current.leave())
    adv(CLOSE_DELAY_MS)
    expect(result.current.activeKey).toBeNull()
  })

  it('focus opens the tooltip, except right after suppressFocus', () => {
    const { result } = renderHook(() => useHoverIntent())
    act(() => result.current.focus('a'))
    expect(result.current.activeKey).toBe('a')
    act(() => result.current.hide())
    act(() => result.current.suppressFocus(100))
    act(() => result.current.focus('a'))
    expect(result.current.activeKey).toBeNull()
    adv(100)
    act(() => result.current.focus('a'))
    expect(result.current.activeKey).toBe('a')
  })

  it('clears timers on unmount', () => {
    const { result, unmount } = renderHook(() => useHoverIntent())
    act(() => result.current.enter('a'))
    unmount()
    expect(vi.getTimerCount()).toBe(0)
  })
})
