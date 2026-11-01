// @vitest-environment jsdom
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'

import { AuthContext, type AuthValue } from '@/features/auth/auth-context'
import { makeDevice } from '@/lib/fixtures'
import type { Device, Role } from '@/lib/types'
import { MapTooltip } from './MapTooltip'

afterEach(cleanup)
const NOW = 1_000_060

function auth(role: Role): AuthValue {
  return {
    status: 'authenticated',
    user: {
      username: 'u',
      role,
      must_change_password: false,
      totp_enabled: false,
      csrf_token: 't',
    },
    isAdmin: role === 'admin',
    mustChangePassword: false,
    error: null,
    retry: () => {},
    login: () => Promise.resolve(),
    verifyMfa: () => Promise.resolve(),
    logout: () => Promise.resolve(),
    changePassword: () => Promise.resolve(),
  }
}

function show(role: Role, device: Device = makeDevice(), geocodeEnabled = false, now = NOW) {
  return render(
    <QueryClientProvider client={new QueryClient()}>
      <AuthContext.Provider value={auth(role)}>
        <MapTooltip device={device} now={now} geocodeEnabled={geocodeEnabled} />
      </AuthContext.Provider>
    </QueryClientProvider>,
  )
}

describe('MapTooltip', () => {
  it('shows Ping to admins only', () => {
    show('admin')
    expect(screen.getByRole('button', { name: 'Ping Pixel' })).toBeTruthy()
    cleanup()
    show('viewer')
    expect(screen.queryByRole('button', { name: /Ping/ })).toBeNull()
  })

  it('links to the history page for admins only', () => {
    show('admin')
    expect(
      screen.getByRole('link', { name: /Location history of Pixel/ }).getAttribute('href'),
    ).toBe('/devices/dradis/pixel/history')
    cleanup()
    show('viewer')
    expect(screen.queryByRole('link', { name: /Location history/ })).toBeNull()
  })

  it('shows the safe place badge before the address, or before the coordinates', () => {
    const safe_place = { id: 1, name: 'Home' }
    show('viewer', makeDevice({ safe_place, location_name: 'Dizengoff St, Tel Aviv' }))
    expect(screen.getByTestId('place-badge').textContent).toBe('Home')
    expect(screen.getByText('Dizengoff St, Tel Aviv')).toBeTruthy()
    cleanup()
    show('viewer', makeDevice({ safe_place }))
    expect(screen.getByTestId('place-badge').textContent).toBe('Home')
    expect(screen.getByText(/32\.0853, 34\.7818/)).toBeTruthy()
    cleanup()
    show('viewer', makeDevice())
    expect(screen.queryByTestId('place-badge')).toBeNull()
  })

  it('shows name, battery, charging, relative time and a Google Maps link', () => {
    show('viewer', makeDevice({ charging: true, battery_level: 15 }))
    expect(screen.getByText('Pixel')).toBeTruthy()
    expect(screen.getByText('15%')).toBeTruthy()
    expect(screen.getByText('Charging')).toBeTruthy()
    expect(screen.getByText('Low battery')).toBeTruthy()
    expect(screen.getByText('Last updated 1 min ago')).toBeTruthy()
    const link = screen.getByRole('link', { name: /Open in Google Maps/ })
    expect(link.getAttribute('href')).toBe(
      'https://www.google.com/maps/search/?api=1&query=32.08531,34.78176',
    )
    expect(link.getAttribute('rel')).toBe('noopener noreferrer')
  })

  it('uses the location name (dir=auto) with coordinates underneath', () => {
    show('viewer', makeDevice({ location_name: 'רוטשילד, תל אביב' }))
    const n = screen.getByText('רוטשילד, תל אביב')
    expect(n.getAttribute('dir')).toBe('auto')
    expect(screen.getByText('32.0853, 34.7818')).toBeTruthy()
  })

  it('shows a skeleton while the name loads, and coordinates otherwise', () => {
    show('viewer', makeDevice({ location_time: NOW - 1 }), true)
    expect(screen.getByRole('status', { name: 'Looking up place name' })).toBeTruthy()
    cleanup()
    show('viewer', makeDevice({ location_time: NOW - 600 }), true)
    expect(screen.queryByRole('status')).toBeNull()
    expect(screen.getByText('32.0853, 34.7818')).toBeTruthy()
  })
})
