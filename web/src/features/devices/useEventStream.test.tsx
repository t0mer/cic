// @vitest-environment jsdom
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, cleanup, renderHook } from '@testing-library/react'
import type { ReactNode } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { toast } from 'sonner'
import { clearResults, getResultForTest, registerIssued } from '@/features/commands/results'
import { authKey, brokerKey, devicesKey } from '@/lib/api'
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
    const { result } = renderHook(() => useEventStream(true), { wrapper })
    expect(result.current).toBe('connecting')
    act(() => last().open())
    expect(result.current).toBe('live')
  })

  it('upserts on device.updated', () => {
    qc.setQueryData<Device[]>(devicesKey, [makeDevice({ key: 'a/1', online: true })])
    renderHook(() => useEventStream(true), { wrapper })
    act(() => last().emit('device.updated', makeDevice({ key: 'a/1', online: false })))
    act(() => last().emit('device.updated', makeDevice({ key: 'a/2' })))
    expect(devices()?.map((d) => [d.key, d.online])).toEqual([
      ['a/1', false],
      ['a/2', true],
    ])
  })

  it('removes a device that becomes hidden', () => {
    qc.setQueryData<Device[]>(devicesKey, [makeDevice({ key: 'a/1' }), makeDevice({ key: 'a/2' })])
    renderHook(() => useEventStream(true), { wrapper })
    act(() => last().emit('device.updated', makeDevice({ key: 'a/1', hidden: true })))
    expect(devices()?.map((d) => d.key)).toEqual(['a/2'])
  })

  it('does not add a hidden device that was unknown', () => {
    qc.setQueryData<Device[]>(devicesKey, [makeDevice({ key: 'a/1' })])
    renderHook(() => useEventStream(true), { wrapper })
    act(() => last().emit('device.updated', makeDevice({ key: 'a/9', hidden: true })))
    expect(devices()?.map((d) => d.key)).toEqual(['a/1'])
  })

  it('removes on device.removed', () => {
    qc.setQueryData<Device[]>(devicesKey, [makeDevice({ key: 'a/1' }), makeDevice({ key: 'a/2' })])
    renderHook(() => useEventStream(true), { wrapper })
    act(() => last().emit('device.removed', { key: 'a/1' }))
    expect(devices()?.map((d) => d.key)).toEqual(['a/2'])
  })

  it('ignores malformed payloads', () => {
    qc.setQueryData<Device[]>(devicesKey, [makeDevice({ key: 'a/1' })])
    renderHook(() => useEventStream(true), { wrapper })
    act(() => last().emit('device.removed', undefined))
    expect(devices()).toHaveLength(1)
  })

  it('patches the broker query on broker.state', () => {
    renderHook(() => useEventStream(true), { wrapper })
    const b: Broker = { connected: false, host: 'h:1', last_error: 'boom', since: null }
    act(() => last().emit('broker.state', b))
    expect(qc.getQueryData(brokerKey)).toEqual(b)
  })

  it('does not invalidate on the first open but does on a re-open', () => {
    const spy = vi.spyOn(qc, 'invalidateQueries')
    renderHook(() => useEventStream(true), { wrapper })
    act(() => last().open())
    expect(spy).not.toHaveBeenCalled()

    act(() => last().fail())
    act(() => void vi.advanceTimersByTime(1000))
    expect(FakeES.instances).toHaveLength(2)
    act(() => last().open())
    expect(spy.mock.calls.filter((c) => c[0]?.queryKey === devicesKey)).toHaveLength(1)
  })

  it('reconnects with capped exponential backoff and resets after an open', () => {
    const { result } = renderHook(() => useEventStream(true), { wrapper })
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
    const { unmount } = renderHook(() => useEventStream(true), { wrapper })
    const first = last()
    act(() => first.fail())
    expect(vi.getTimerCount()).toBe(1)
    unmount()
    expect(vi.getTimerCount()).toBe(0)
    act(() => void vi.advanceTimersByTime(60_000))
    expect(FakeES.instances).toHaveLength(1)

    const { unmount: unmount2 } = renderHook(() => useEventStream(true), { wrapper })
    const live = last()
    unmount2()
    expect(live.closed).toBe(true)
  })

  it('does not connect while logged out', () => {
    renderHook(() => useEventStream(false), { wrapper })
    expect(FakeES.instances).toHaveLength(0)
  })

  it('closes on logout and does not reconnect afterwards', () => {
    const { rerender } = renderHook(({ on }) => useEventStream(on), {
      wrapper,
      initialProps: { on: true },
    })
    const first = last()
    act(() => first.fail())
    expect(vi.getTimerCount()).toBe(1)
    rerender({ on: false })
    expect(first.closed).toBe(true)
    expect(vi.getTimerCount()).toBe(0)
    act(() => void vi.advanceTimersByTime(60_000))
    expect(FakeES.instances).toHaveLength(1)
  })

  it('re-checks the session when the stream is closed (401) instead of looping', () => {
    const spy = vi.spyOn(qc, 'invalidateQueries')
    const { rerender } = renderHook(({ on }) => useEventStream(on), {
      wrapper,
      initialProps: { on: true },
    })
    act(() => last().fail())
    expect(spy).toHaveBeenCalledWith({ queryKey: authKey })
    // the auth refetch says "anonymous" -> hook disabled before the retry fires
    rerender({ on: false })
    act(() => void vi.advanceTimersByTime(60_000))
    expect(FakeES.instances).toHaveLength(1)
  })

  it('publishes command.result', () => {
    clearResults()
    renderHook(() => useEventStream(true), { wrapper })
    act(() =>
      last().emit('command.result', {
        id: 'abc',
        action: 'sms',
        device: 'a/b',
        status: 'confirmed_ok',
      }),
    )
    expect(getResultForTest('abc')?.status).toBe('confirmed_ok')
  })

  describe('failure toasts', () => {
    const fail = (id: string, action = 'getstatus') => ({
      id,
      action,
      device: 'a/b',
      status: 'failed',
      error: 'nope',
    })
    it('toasts friendly labels only for admins and ids this session issued', () => {
      const spy = vi.spyOn(toast, 'error').mockImplementation(() => 1)
      clearResults()
      registerIssued('mine')
      renderHook(() => useEventStream(true, true), { wrapper })
      act(() => last().emit('command.result', fail('theirs')))
      expect(spy).not.toHaveBeenCalled()
      act(() => last().emit('command.result', fail('mine')))
      expect(spy).toHaveBeenCalledWith('Refresh status failed: nope')
      spy.mockRestore()
    })
    it('does not toast for non-admins', () => {
      const spy = vi.spyOn(toast, 'error').mockImplementation(() => 1)
      clearResults()
      registerIssued('mine')
      renderHook(() => useEventStream(true, false), { wrapper })
      act(() => last().emit('command.result', fail('mine')))
      expect(spy).not.toHaveBeenCalled()
      spy.mockRestore()
    })
    it('resets status to connecting when the stream restarts', () => {
      const { result, rerender } = renderHook(({ on }) => useEventStream(on, true), {
        wrapper,
        initialProps: { on: true },
      })
      act(() => last().open())
      expect(result.current).toBe('live')
      rerender({ on: false })
      rerender({ on: true })
      expect(result.current).toBe('connecting')
    })
  })
})
