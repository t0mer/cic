// @vitest-environment jsdom
import { cleanup, render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { makeDevice } from '@/lib/fixtures'

const state = vi.hoisted(() => ({ current: {} as Record<string, unknown> }))
vi.mock('./queries', () => ({ useDevices: () => state.current }))
vi.mock('@/features/config/queries', () => ({
  useUiConfig: () => ({ data: undefined }),
}))

import { DevicesPage } from './DevicesPage'

afterEach(cleanup)
const base = { data: undefined, isPending: false, error: null, refetch: vi.fn(), isFetching: false }
const renderPage = () =>
  render(
    <MemoryRouter>
      <DevicesPage />
    </MemoryRouter>,
  )

describe('DevicesPage', () => {
  it('announces loading and has a page heading', () => {
    state.current = { ...base, isPending: true }
    renderPage()
    expect(screen.getByRole('status').textContent).toContain('Loading devices')
    expect(screen.getByRole('heading', { level: 1, name: 'Devices' })).toBeTruthy()
  })

  it('shows a non-blocking notice when a refetch fails after a successful load', () => {
    state.current = { ...base, data: [makeDevice()], error: new Error('network down') }
    renderPage()
    expect(screen.getByRole('status').textContent).toBe('Data may be out of date — retrying')
    expect(screen.getAllByTestId('device-card')).toHaveLength(1)
  })

  it('shows no notice when healthy', () => {
    state.current = { ...base, data: [makeDevice()] }
    renderPage()
    expect(screen.queryByRole('status')).toBeNull()
  })

  it('blocks with an error only when there is no data', () => {
    state.current = { ...base, error: new Error('network down') }
    renderPage()
    expect(screen.getByRole('alert').textContent).toBe('network down')
    expect(screen.getByRole('heading', { level: 2, name: 'Could not load devices' })).toBeTruthy()
  })
})
