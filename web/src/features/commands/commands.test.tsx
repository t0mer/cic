// @vitest-environment jsdom
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { AuthContext, type AuthValue } from '@/features/auth/auth-context'
import { DeviceCard } from '@/features/devices/DeviceCard'
import { makeDevice } from '@/lib/fixtures'
import type { Role } from '@/lib/types'
import { clearResults, publishResult } from './results'
import { SmsCounter } from './SmsDialog'
import { SMS_TIMEOUT_MS, SmsStatus } from './SmsStatus'

afterEach(() => {
  cleanup()
  vi.useRealTimers()
  clearResults()
})

function auth(role: Role | null): AuthValue {
  return {
    status: role ? 'authenticated' : 'anonymous',
    user: role ? { username: 'u', role, must_change_password: false, csrf_token: 't' } : null,
    isAdmin: role === 'admin',
    mustChangePassword: false,
    error: null,
    retry: () => {},
    login: () => Promise.resolve(),
    logout: () => Promise.resolve(),
    changePassword: () => Promise.resolve(),
  }
}

function card(role: Role | null, online = true) {
  return render(
    <QueryClientProvider client={new QueryClient()}>
      <AuthContext.Provider value={auth(role)}>
        <DeviceCard device={makeDevice({ online })} now={1_000_060} />
      </AuthContext.Provider>
    </QueryClientProvider>,
  )
}

describe('role-based rendering', () => {
  it('viewers see no action controls', () => {
    card('viewer')
    expect(screen.queryByTestId('device-actions')).toBeNull()
    expect(screen.queryByRole('button')).toBeNull()
  })
  it('anonymous sees no action controls', () => {
    card(null)
    expect(screen.queryByTestId('device-actions')).toBeNull()
  })
  it('admins see the actions', () => {
    card('admin')
    expect(screen.getByRole('button', { name: 'Ping Pixel' })).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Notify Pixel' })).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Send SMS from Pixel' })).toBeTruthy()
    expect(screen.getByRole('button', { name: 'More actions for Pixel' })).toBeTruthy()
  })
  it('an offline device explains itself and asks before pinging', () => {
    card('admin', false)
    const ping = screen.getByRole('button', { name: 'Ping Pixel' })
    expect(ping.getAttribute('aria-disabled')).toBe('true')
    expect(ping.getAttribute('title')).toMatch(/offline/)
    fireEvent.click(ping)
    expect(screen.getByText('Send anyway')).toBeTruthy()
  })
})

describe('SmsStatus', () => {
  beforeEach(() => vi.useFakeTimers())

  it('starts pending and shows Delivered when confirmed', () => {
    render(<SmsStatus id="s1" />)
    expect(screen.getByRole('status').textContent).toBe('Sending SMS…')
    act(() => publishResult({ id: 's1', action: 'sms', device: 'a/b', status: 'confirmed_ok' }))
    expect(screen.getByRole('status').textContent).toBe('Delivered')
  })
  it('shows the failure text', () => {
    render(<SmsStatus id="s2" />)
    act(() =>
      publishResult({ id: 's2', action: 'sms', device: 'a/b', status: 'failed', error: 'no SIM' }),
    )
    expect(screen.getByRole('status').textContent).toBe('SMS failed: no SIM')
  })
  it('ignores results for other ids', () => {
    render(<SmsStatus id="s3" />)
    act(() => publishResult({ id: 'other', action: 'sms', device: 'a/b', status: 'confirmed_ok' }))
    expect(screen.getByRole('status').textContent).toBe('Sending SMS…')
  })
  it('uses a result that arrived before the component mounted', () => {
    publishResult({ id: 's4', action: 'sms', device: 'a/b', status: 'no_confirmation' })
    render(<SmsStatus id="s4" />)
    expect(screen.getByRole('status').textContent).toBe('Sent, no confirmation')
  })
  it('falls back to "Sent, no confirmation" after 60 s', () => {
    render(<SmsStatus id="s5" />)
    act(() => void vi.advanceTimersByTime(SMS_TIMEOUT_MS - 1))
    expect(screen.getByRole('status').textContent).toBe('Sending SMS…')
    act(() => void vi.advanceTimersByTime(1))
    expect(screen.getByRole('status').textContent).toBe('Sent, no confirmation')
  })
})

describe('SmsCounter', () => {
  it('shows characters, segments and encoding', () => {
    render(<SmsCounter text={'a'.repeat(161)} />)
    expect(screen.getByTestId('sms-counter').textContent).toBe(
      '161 characters · 2 segments · GSM-7',
    )
    cleanup()
    render(<SmsCounter text="שלום" />)
    expect(screen.getByTestId('sms-counter').textContent).toBe('4 characters · 1 segment · Unicode')
  })
})
