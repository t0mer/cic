// @vitest-environment jsdom
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { toast } from 'sonner'

import { setCsrfToken } from '@/lib/api'
import { makeDevice } from '@/lib/fixtures'
import { PING_STOP_MS, PingControl } from './PingControl'
import { resetPingState } from './ping-state'
import { clearResults } from './results'

vi.mock('sonner', () => ({
  toast: Object.assign(vi.fn(), { success: vi.fn(), error: vi.fn(), warning: vi.fn() }),
}))

const device = makeDevice({ online: true })
let fetchMock: ReturnType<typeof vi.fn>
let release: (r: Response) => void = () => {}
let qc: QueryClient

const wrap = (ui: React.ReactNode) =>
  render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
const pendingFetch = () =>
  fetchMock.mockImplementation(() => new Promise<Response>((r) => (release = r)))
const ack = () => release(new Response(JSON.stringify({ id: 'p1' }), { status: 202 }))

beforeEach(() => {
  qc = new QueryClient({ defaultOptions: { mutations: { retry: false } } })
  fetchMock = vi.fn()
  vi.stubGlobal('fetch', fetchMock)
  setCsrfToken('t')
  vi.mocked(toast.success).mockClear()
})
afterEach(() => {
  cleanup()
  vi.useRealTimers()
  vi.unstubAllGlobals()
  resetPingState()
  clearResults()
})

describe('PingControl', () => {
  it('survives the tooltip unmounting before the ack: toast fires and Stop shows on remount', async () => {
    pendingFetch()
    const first = wrap(<PingControl device={device} />)
    fireEvent.click(screen.getByRole('button', { name: 'Ping Pixel' }))
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1))
    first.unmount()
    await act(async () => ack())
    await waitFor(() => expect(toast.success).toHaveBeenCalledWith('Ping sent to Pixel'))
    wrap(<PingControl device={device} />)
    expect(screen.getByRole('button', { name: 'Stop ping on Pixel' })).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Ping Pixel' }).textContent).toContain('Sent')
  })

  it('blocks a second send while the first is in flight, even from a remounted control', async () => {
    pendingFetch()
    const first = wrap(<PingControl device={device} />)
    fireEvent.click(screen.getByRole('button', { name: 'Ping Pixel' }))
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1))
    first.unmount()
    wrap(<PingControl device={device} />)
    const btn = screen.getByRole('button', { name: 'Ping Pixel' }) as HTMLButtonElement
    expect(btn.disabled).toBe(true)
    fireEvent.click(btn)
    expect(fetchMock).toHaveBeenCalledTimes(1)
    await act(async () => ack())
  })

  it('allows another ping after a failure', async () => {
    fetchMock.mockResolvedValue(new Response(JSON.stringify({ error: 'nope' }), { status: 400 }))
    wrap(<PingControl device={device} />)
    fireEvent.click(screen.getByRole('button', { name: 'Ping Pixel' }))
    await waitFor(() => expect(toast.error).toHaveBeenCalled())
    await waitFor(() =>
      expect(
        (screen.getByRole('button', { name: 'Ping Pixel' }) as HTMLButtonElement).disabled,
      ).toBe(false),
    )
  })

  it('holds the host open while pinging and while Stop shows, then releases it', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    pendingFetch()
    const hold = vi.fn()
    wrap(<PingControl device={device} onConfirmOpenChange={hold} />)
    expect(hold).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole('button', { name: 'Ping Pixel' }))
    await waitFor(() => expect(hold).toHaveBeenLastCalledWith(true))
    await act(async () => ack())
    await screen.findByRole('button', { name: 'Stop ping on Pixel' })
    expect(hold).not.toHaveBeenLastCalledWith(false)
    act(() => void vi.advanceTimersByTime(PING_STOP_MS + 50))
    await waitFor(() => expect(hold).toHaveBeenLastCalledWith(false))
  })

  it('releases the hold when it unmounts while holding', async () => {
    pendingFetch()
    const hold = vi.fn()
    const view = wrap(<PingControl device={device} onConfirmOpenChange={hold} />)
    fireEvent.click(screen.getByRole('button', { name: 'Ping Pixel' }))
    await waitFor(() => expect(hold).toHaveBeenLastCalledWith(true))
    view.unmount()
    expect(hold).toHaveBeenLastCalledWith(false)
    await act(async () => ack())
  })
})
