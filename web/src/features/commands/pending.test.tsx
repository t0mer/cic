// @vitest-environment jsdom
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { useEffect, useState } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { toast } from 'sonner'

import { DeviceActions, UNKNOWN_HOLD_MS } from './DeviceActions'
import { clearResults, publishResult } from './results'
import { SmsDialog, previewText } from './SmsDialog'
import { SMS_TIMEOUT_MS, SmsStatus, SmsStatusList } from './SmsStatus'
import { useCommand } from './useCommand'
import { makeDevice } from '@/lib/fixtures'
import { setCsrfToken } from '@/lib/api'

vi.mock('sonner', () => ({
  toast: Object.assign(vi.fn(), {
    success: vi.fn(),
    error: vi.fn(),
    warning: vi.fn(),
  }),
}))

const device = makeDevice({ online: true })
let fetchMock: ReturnType<typeof vi.fn>
let release: (r: Response) => void = () => {}
let qc: QueryClient

const wrap = (ui: React.ReactNode) =>
  render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
const pendingFetch = () =>
  fetchMock.mockImplementation(() => new Promise<Response>((r) => (release = r)))

beforeEach(() => {
  qc = new QueryClient({ defaultOptions: { mutations: { retry: false } } })
  fetchMock = vi.fn()
  vi.stubGlobal('fetch', fetchMock)
  setCsrfToken('t')
  vi.mocked(toast.success).mockClear()
  vi.mocked(toast.warning).mockClear()
})
afterEach(() => {
  cleanup()
  vi.useRealTimers()
  vi.unstubAllGlobals()
  clearResults()
})

function Harness({ onSent }: { onSent: (id: string) => void }) {
  const [open, setOpen] = useState(true)
  return (
    <>
      <span data-testid="open">{String(open)}</span>
      <SmsDialog device={device} open={open} onOpenChange={setOpen} onSent={onSent} />
    </>
  )
}

function fillAndReview() {
  fireEvent.change(screen.getByLabelText('Phone number'), { target: { value: '+972 50-123 4567' } })
  fireEvent.change(screen.getByLabelText('Message'), { target: { value: 'hello there' } })
  fireEvent.click(screen.getByRole('button', { name: 'Review' }))
}

describe('SMS dialog', () => {
  it('confirm step shows the full normalized number and a text preview', () => {
    wrap(<Harness onSent={() => {}} />)
    fillAndReview()
    const n = screen.getByTestId('sms-confirm-number')
    expect(n.textContent).toBe('+972501234567')
    expect(n.getAttribute('dir')).toBe('ltr')
    expect(screen.getByTestId('sms-confirm-text').textContent).toBe('hello there')
    expect(screen.getByTestId('sms-confirm-text').getAttribute('dir')).toBe('auto')
    expect(screen.getByTestId('sms-confirm-info').textContent).toMatch(/1 segment/)
  })
  it('previewText cuts at 160 characters with an ellipsis', () => {
    expect(previewText('a'.repeat(160))).toBe('a'.repeat(160))
    expect(previewText('a'.repeat(161))).toBe('a'.repeat(160) + '…')
  })

  it('ignores close attempts while pending, then registers success', async () => {
    pendingFetch()
    const onSent = vi.fn()
    wrap(<Harness onSent={onSent} />)
    fillAndReview()
    fireEvent.click(screen.getByRole('button', { name: 'Send SMS' }))
    await waitFor(() => expect(screen.getByRole('button', { name: 'Sending…' })).toBeTruthy())
    fireEvent.keyDown(screen.getByTestId('dialog'), { key: 'Escape' })
    expect((screen.getByRole('button', { name: 'Close' }) as HTMLButtonElement).disabled).toBe(true)
    fireEvent.click(screen.getByRole('button', { name: 'Close' }))
    expect(screen.getByTestId('open').textContent).toBe('true')
    await act(async () => release(new Response(JSON.stringify({ id: 'c9' }), { status: 202 })))
    await waitFor(() => expect(onSent).toHaveBeenCalledWith('c9'))
    expect(toast.success).toHaveBeenCalled()
    expect(screen.getByTestId('open').textContent).toBe('false')
  })
})

describe('hook-level success survives unmount', () => {
  it('still runs onAck after the component is gone', async () => {
    pendingFetch()
    const onAck = vi.fn()
    const h: { mutate: (b: unknown) => void } = { mutate: () => {} }
    function C() {
      const c = useCommand(device, 'ping', { onAck })
      useEffect(() => {
        h.mutate = c.mutate
      })
      return null
    }
    const { unmount } = wrap(<C />)
    act(() => h.mutate({}))
    await waitFor(() => expect(fetchMock).toHaveBeenCalled())
    unmount()
    await act(async () => release(new Response(JSON.stringify({ id: 'z1' }), { status: 202 })))
    await waitFor(() => expect(onAck).toHaveBeenCalledTimes(1))
  })
})

describe('outcome_unknown UX', () => {
  it('closes, resets the draft, shows a persistent notice and disables SMS for 30 s', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    fetchMock.mockResolvedValue(
      new Response(JSON.stringify({ error: 'x', code: 'outcome_unknown', retry: false }), {
        status: 504,
      }),
    )
    wrap(<DeviceActions device={device} />)
    const smsBtn = () => screen.getByRole('button', { name: /Send SMS from/ }) as HTMLButtonElement
    fireEvent.click(smsBtn())
    fillAndReview()
    fireEvent.click(screen.getByRole('button', { name: 'Send SMS' }))
    const notice = await screen.findByTestId('unsure-notice')
    expect(notice.textContent).toMatch(/may still arrive/)
    expect(notice.textContent).toMatch(/Do not resend/)
    expect(screen.queryByTestId('dialog')).toBeNull()
    expect(smsBtn().disabled).toBe(true)
    // persistent: not dismissed by time, only the hold expires
    act(() => void vi.advanceTimersByTime(UNKNOWN_HOLD_MS))
    expect(smsBtn().disabled).toBe(false)
    expect(screen.getByTestId('unsure-notice')).toBeTruthy()
    // reopening shows an empty draft
    fireEvent.click(smsBtn())
    expect((screen.getByLabelText('Phone number') as HTMLInputElement).value).toBe('')
    cleanup()
    // acknowledging clears it
  })
  it('acknowledging dismisses the notice and releases the hold', async () => {
    fetchMock.mockResolvedValue(
      new Response(JSON.stringify({ error: 'x', code: 'outcome_unknown' }), { status: 504 }),
    )
    wrap(<DeviceActions device={device} />)
    fireEvent.click(screen.getByRole('button', { name: /Notify/ }))
    fireEvent.change(screen.getByLabelText('Message'), { target: { value: 'hi' } })
    fireEvent.click(screen.getByRole('button', { name: 'Send notification' }))
    await screen.findByTestId('unsure-notice')
    fireEvent.click(screen.getByRole('button', { name: 'I understand' }))
    expect(screen.queryByTestId('unsure-notice')).toBeNull()
    expect((screen.getByRole('button', { name: /Notify/ }) as HTMLButtonElement).disabled).toBe(
      false,
    )
  })
})

describe('SmsStatus identity and lists', () => {
  beforeEach(() => vi.useFakeTimers())

  it('resets when the id changes (timed-out state does not leak)', () => {
    const { rerender } = render(<SmsStatus id="a" />)
    act(() => void vi.advanceTimersByTime(SMS_TIMEOUT_MS))
    expect(screen.getByRole('status').textContent).toBe('Sent, no confirmation')
    rerender(<SmsStatus id="b" />)
    expect(screen.getByRole('status').textContent).toBe('Sending SMS…')
  })
  it('A timing out does not affect pending B, and A lingering does not clear B', () => {
    const finished = vi.fn()
    const { rerender } = render(<SmsStatusList ids={['a']} onFinished={finished} />)
    act(() => void vi.advanceTimersByTime(30_000))
    rerender(<SmsStatusList ids={['b', 'a']} onFinished={finished} />)
    expect(screen.getAllByRole('status')).toHaveLength(2)
    act(() => void vi.advanceTimersByTime(30_000)) // A times out at 60 s
    const [b, a] = screen.getAllByRole('status')
    expect(b.textContent).toBe('Sending SMS…')
    expect(a.textContent).toBe('Sent, no confirmation')
    act(() => void vi.advanceTimersByTime(15_000)) // A lingers out
    expect(finished).toHaveBeenCalledTimes(1)
    expect(finished).toHaveBeenCalledWith('a')
  })
  it('clears timers on unmount', () => {
    const finished = vi.fn()
    const { unmount } = render(<SmsStatus id="a" onFinished={finished} />)
    act(() => void vi.advanceTimersByTime(SMS_TIMEOUT_MS - 1))
    unmount()
    act(() => void vi.advanceTimersByTime(120_000))
    expect(finished).not.toHaveBeenCalled()
  })
  it('DeviceActions keeps the latest three sends', async () => {
    vi.useRealTimers()
    let n = 0
    fetchMock.mockImplementation(() =>
      Promise.resolve(new Response(JSON.stringify({ id: `s${++n}` }), { status: 202 })),
    )
    wrap(<DeviceActions device={device} />)
    for (let i = 0; i < 4; i++) {
      fireEvent.click(screen.getByRole('button', { name: /Send SMS from/ }))
      fillAndReview()
      fireEvent.click(screen.getByRole('button', { name: 'Send SMS' }))
      await waitFor(() => expect(screen.queryByTestId('dialog')).toBeNull())
    }
    expect(screen.getAllByTestId('sms-status')).toHaveLength(3)
  })
  it('publishResult for B only touches B', () => {
    render(<SmsStatusList ids={['b', 'a']} onFinished={() => {}} />)
    act(() => publishResult({ id: 'b', action: 'sms', device: 'x/y', status: 'confirmed_ok' }))
    const [b, a] = screen.getAllByRole('status')
    expect(b.textContent).toBe('Delivered')
    expect(a.textContent).toBe('Sending SMS…')
  })
})
