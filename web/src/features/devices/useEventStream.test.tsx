// @vitest-environment jsdom
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, cleanup, renderHook } from '@testing-library/react'
import type { ReactNode } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { brokerKey, devicesKey } from '@/lib/api'
import { makeDevice } from '@/lib/fixtures'
import type { Broker, Device } from '@/lib/types'
import { useEventStream } from './useEventStream'

class FakeES {
  static CLOSED = 2
  static instances: FakeES[] = []
  readyState = 0
  onopen: ((e: Event) => void) | null = null
  onerror: ((e: Event) => void) | null = null
  closed = false
  private listeners = new Map<string, ((e: Event) => void)[]>()
  constructor(public url: string) {
    FakeES.instances.push(this)
  }
  addEventListener(type: string, fn: (e: Event) => void) {
    this.listeners.set(type, [...(this.listeners.get(type) ?? []), fn])
  }
  close() {
    this.readyState = FakeES.CLOSED
    this.closed = true
  }
  open() {
    this.readyState = 1
    this.onopen?.(new Event('open'))
  }
  fail() {
    this.readyState = FakeES.CLOSED
    this.onerror?.(new Event('error'))
  }
  emit(type: string, data: unknown) {
    const e = new MessageEvent(type, { data: JSON.stringify(data) })
    this.listeners.get(type)?.forEach((fn) => fn(e))
  }
}

const last = () => FakeES.instances[FakeES.instances.length - 1]

let qc: QueryClient
const wrapper = ({ children }: { children: ReactNode }) => (
  <QueryClientProvider client={qc}>{children}</QueryClientProvider>
)

beforeEach(() => {
  vi.useFakeTimers()
  FakeES.instances = []
  vi.stubGlobal('EventSource', FakeES)
  qc = new QueryClient()
})
afterEach(() => {
  cleanup()
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

const devices = () => qc.getQueryData<Device[]>(devicesKey)

describe('useEventStream', () => {
  it('goes live on open', () => {
    const { result } = renderHook(() => useEventStream(), { wrapper })
    expect(result.current).toBe('connecting')
    act(() => last().open())
    expect(result.current).toBe('live')
  })

  it('upserts on device.updated', () => {
    qc.setQueryData<Device[]>(devicesKey, [makeDevice({ key: 'a/1', online: true })])
    renderHook(() => useEventStream(), { wrapper })
    act(() => last().emit('device.updated', makeDevice({ key: 'a/1', online: false })))
    act(() => last().emit('device.updated', makeDevice({ key: 'a/2' })))
    expect(devices()?.map((d) => [d.key, d.online])).toEqual([
      ['a/1', false],
      ['a/2', true],
    ])
  })

  it('removes a device that becomes hidden', () => {
    qc.setQueryData<Device[]>(devicesKey, [makeDevice({ key: 'a/1' }), makeDevice({ key: 'a/2' })])
    renderHook(() => useEventStream(), { wrapper })
    act(() => last().emit('device.updated', makeDevice({ key: 'a/1', hidden: true })))
    expect(devices()?.map((d) => d.key)).toEqual(['a/2'])
  })

  it('does not add a hidden device that was unknown', () => {
    qc.setQueryData<Device[]>(devicesKey, [makeDevice({ key: 'a/1' })])
    renderHook(() => useEventStream(), { wrapper })
    act(() => last().emit('device.updated', makeDevice({ key: 'a/9', hidden: true })))
    expect(devices()?.map((d) => d.key)).toEqual(['a/1'])
  })

  it('removes on device.removed', () => {
    qc.setQueryData<Device[]>(devicesKey, [makeDevice({ key: 'a/1' }), makeDevice({ key: 'a/2' })])
    renderHook(() => useEventStream(), { wrapper })
    act(() => last().emit('device.removed', { key: 'a/1' }))
    expect(devices()?.map((d) => d.key)).toEqual(['a/2'])
  })

  it('ignores malformed payloads', () => {
    qc.setQueryData<Device[]>(devicesKey, [makeDevice({ key: 'a/1' })])
    renderHook(() => useEventStream(), { wrapper })
    act(() => last().emit('device.removed', undefined))
    expect(devices()).toHaveLength(1)
  })

  it('patches the broker query on broker.state', () => {
    renderHook(() => useEventStream(), { wrapper })
    const b: Broker = { connected: false, host: 'h:1', last_error: 'boom', since: null }
    act(() => last().emit('broker.state', b))
    expect(qc.getQueryData(brokerKey)).toEqual(b)
  })

  it('does not invalidate on the first open but does on a re-open', () => {
    const spy = vi.spyOn(qc, 'invalidateQueries')
    renderHook(() => useEventStream(), { wrapper })
    act(() => last().open())
    expect(spy).not.toHaveBeenCalled()

    act(() => last().fail())
    act(() => void vi.advanceTimersByTime(1000))
    expect(FakeES.instances).toHaveLength(2)
    act(() => last().open())
    expect(spy).toHaveBeenCalledTimes(1)
    expect(spy).toHaveBeenCalledWith({ queryKey: devicesKey })
  })

  it('reconnects with capped exponential backoff and resets after an open', () => {
    const { result } = renderHook(() => useEventStream(), { wrapper })
    for (const delay of [1000, 2000, 4000, 8000, 15_000, 15_000]) {
      const before = FakeES.instances.length
      act(() => last().fail())
      expect(result.current).toBe('reconnecting')
      act(() => void vi.advanceTimersByTime(delay - 1))
      expect(FakeES.instances).toHaveLength(before)
      act(() => void vi.advanceTimersByTime(1))
      expect(FakeES.instances).toHaveLength(before + 1)
    }
    act(() => last().open())
    expect(result.current).toBe('live')
    const before = FakeES.instances.length
    act(() => last().fail())
    act(() => void vi.advanceTimersByTime(1000))
    expect(FakeES.instances).toHaveLength(before + 1)
  })

  it('closes the source and clears the retry timer on unmount', () => {
    const { unmount } = renderHook(() => useEventStream(), { wrapper })
    const first = last()
    act(() => first.fail())
    expect(vi.getTimerCount()).toBe(1)
    unmount()
    expect(vi.getTimerCount()).toBe(0)
    act(() => void vi.advanceTimersByTime(60_000))
    expect(FakeES.instances).toHaveLength(1)

    const { unmount: unmount2 } = renderHook(() => useEventStream(), { wrapper })
    const live = last()
    unmount2()
    expect(live.closed).toBe(true)
  })
})
