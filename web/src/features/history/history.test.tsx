// @vitest-environment jsdom
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { AuthContext } from '@/features/auth/auth-context'
import { DeviceCard } from '@/features/devices/DeviceCard'
import { auth, stubApi, type Call } from '@/features/settings/test-utils'
import { makeDevice } from '@/lib/fixtures'
import type { Role, RouteDay } from '@/lib/types'
import { HistoryPage } from './HistoryPage'

// The real map needs WebGL; the page logic is what is under test here.
vi.mock('./HistoryMap', () => ({
  default: ({ days }: { days: { date: string }[] }) => (
    <div data-testid="fake-map">{days.map((d) => d.date).join(',')}</div>
  ),
}))

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

const T0 = 1_760_000_000
const days: RouteDay[] = [
  {
    date: '2026-10-08',
    distance_m: 2400,
    point_count: 3,
    first: T0,
    last: T0 + 600,
    segments: [
      [
        [32.08, 34.78, T0],
        [32.09, 34.79, T0 + 600],
      ],
    ],
  },
  {
    date: '2026-10-09',
    distance_m: 500,
    point_count: 2,
    first: T0 + 86400,
    last: T0 + 86500,
    segments: [[[32.1, 34.8, T0 + 86400]]],
  },
]
const points = Array.from({ length: 120 }, (_, i) => ({
  t: T0 + i * 60,
  lat: 32 + i / 1000,
  lon: 34.7,
  accuracy: i % 2 ? null : 12,
}))

const cfg = (history_enabled = true) => ({
  map: {
    provider: 'x',
    style_light: 'https://a.test/l',
    style_dark: 'https://a.test/d',
    attribution: '',
  },
  geocode_enabled: false,
  history_enabled,
})

interface Opts {
  historyEnabled?: boolean
  routes?: { status?: number; json: unknown }
  history?: { status?: number; json: unknown }
}

function api(o: Opts = {}): Call[] {
  return stubApi((c) => {
    if (c.url === '/api/ui-config') return { json: cfg(o.historyEnabled ?? true) }
    if (c.url === '/api/devices') return { json: [makeDevice({ full_name: 'Tomer Pixel' })] }
    if (c.url.includes('/routes')) return o.routes ?? { json: { days } }
    if (c.url.includes('/history')) return o.history ?? { json: { points, truncated: false } }
    return { status: 404, json: { error: 'not found' } }
  })
}

function show(role: Role | null, entry = '/devices/dradis/pixel/history') {
  return render(
    <QueryClientProvider
      client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}
    >
      <AuthContext.Provider value={auth(role)}>
        <MemoryRouter initialEntries={[entry]}>
          <Routes>
            <Route path="/devices/:prefix/:device/history" element={<HistoryPage />} />
            <Route path="/devices" element={<p>devices page</p>} />
          </Routes>
        </MemoryRouter>
      </AuthContext.Provider>
    </QueryClientProvider>,
  )
}

describe('HistoryPage', () => {
  it('shows a day row per day, the map, a legend and sends the browser tz', async () => {
    const calls = api()
    show('admin')
    expect(await screen.findAllByTestId('day-row')).toHaveLength(2)
    expect((await screen.findByTestId('fake-map')).textContent).toBe('2026-10-08,2026-10-09')
    expect(screen.getByText('Tomer Pixel', { exact: false })).toBeTruthy()
    expect(within(screen.getByTestId('history-legend')).getByText('Start')).toBeTruthy()
    expect(screen.getByText(/2\.4 km/)).toBeTruthy()
    const routes = calls.find((c) => c.url.includes('/routes'))!
    const q = new URL(routes.url, 'http://x').searchParams
    expect(q.get('tz')).toBe(Intl.DateTimeFormat().resolvedOptions().timeZone)
    expect(Date.parse(q.get('from')!)).toBeLessThan(Date.parse(q.get('to')!))
    expect(screen.getByRole('link', { name: /Back to devices/ })).toBeTruthy()
  })

  it('toggles a day off the map', async () => {
    api()
    show('admin')
    await screen.findAllByTestId('day-row')
    await screen.findByTestId('fake-map')
    fireEvent.click(screen.getAllByRole('button', { name: /^Hide .* on the map$/ })[0])
    expect(screen.getByTestId('fake-map').textContent).toBe('2026-10-09')
  })

  it('stores the range in the URL-driven inputs and refetches on a preset', async () => {
    const calls = api()
    show('admin', '/devices/dradis/pixel/history?from=2026-10-01T00:00&to=2026-10-03T00:00')
    await screen.findAllByTestId('day-row')
    expect((screen.getByLabelText('From') as HTMLInputElement).value).toBe('2026-10-01T00:00')
    const before = calls.filter((c) => c.url.includes('/routes')).length
    fireEvent.click(screen.getByRole('button', { name: 'Last 7 days' }))
    await waitFor(() =>
      expect(calls.filter((c) => c.url.includes('/routes')).length).toBe(before + 1),
    )
    expect(screen.getByRole('button', { name: 'Last 7 days' }).getAttribute('aria-pressed')).toBe(
      'true',
    )
  })

  it('does not query an invalid range and says why', async () => {
    const calls = api()
    show('admin', '/devices/dradis/pixel/history?from=2026-10-05T00:00&to=2026-10-01T00:00')
    expect((await screen.findByRole('alert')).textContent).toMatch(/start must be before/)
    expect(calls.some((c) => c.url.includes('/routes'))).toBe(false)
  })

  it('rejects a range over 31 days', async () => {
    api()
    show('admin', '/devices/dradis/pixel/history?from=2026-08-01T00:00&to=2026-10-01T00:00')
    expect((await screen.findByRole('alert')).textContent).toMatch(/31 days/)
  })

  it('shows the empty state', async () => {
    api({ routes: { json: { days: [] } } })
    show('admin')
    expect(await screen.findByText('No locations recorded in this range')).toBeTruthy()
    expect(screen.queryByTestId('fake-map')).toBeNull()
  })

  it('shows an error with retry', async () => {
    let fail = true
    stubApi((c) => {
      if (c.url === '/api/ui-config') return { json: cfg() }
      if (c.url === '/api/devices') return { json: [] }
      if (c.url.includes('/routes'))
        return fail ? { status: 500, json: { error: 'boom' } } : { json: { days } }
      return { json: { points, truncated: false } }
    })
    show('admin')
    expect(await screen.findByText('Could not load history')).toBeTruthy()
    expect(screen.getByRole('alert').textContent).toBe('boom')
    fail = false
    fireEvent.click(screen.getByRole('button', { name: 'Retry' }))
    expect(await screen.findAllByTestId('day-row')).toHaveLength(2)
  })

  it('shows a friendly message on 403', async () => {
    api({ routes: { status: 403, json: { error: 'forbidden' } } })
    show('admin')
    expect(await screen.findByText('You do not have access to location history')).toBeTruthy()
  })

  it('says history is disabled and does not query it', async () => {
    const calls = api({ historyEnabled: false })
    show('admin')
    expect(await screen.findByText('History is disabled')).toBeTruthy()
    expect(calls.some((c) => c.url.includes('/routes') || c.url.includes('/history'))).toBe(false)
  })

  it('blocks viewers without querying', async () => {
    const calls = api()
    show('viewer')
    expect(await screen.findByText(/for administrators only/)).toBeTruthy()
    expect(calls.some((c) => c.url.includes('/routes'))).toBe(false)
  })

  it('pages the raw points and flags a sample', async () => {
    api({ history: { json: { points, truncated: true } } })
    show('admin')
    await screen.findAllByTestId('day-row')
    fireEvent.click(screen.getByRole('tab', { name: 'Points' }))
    expect(await screen.findByText(/Showing a sample of the points/)).toBeTruthy()
    const table = await screen.findByRole('table', { name: 'Recorded points' })
    expect(within(table).getAllByRole('row')).toHaveLength(51) // header + 50
    expect(screen.getByText(/Page 1 of 3 · 120 points/)).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: /Next/ }))
    expect(screen.getByText(/Page 2 of 3/)).toBeTruthy()
  })

  it('shows no sample notice for a complete range', async () => {
    api()
    show('admin')
    await screen.findAllByTestId('day-row')
    fireEvent.click(screen.getByRole('tab', { name: 'Points' }))
    await screen.findByRole('table', { name: 'Recorded points' })
    expect(screen.queryByText(/Showing a sample/)).toBeNull()
  })
})

describe('History entry point', () => {
  const card = (role: Role) =>
    render(
      <QueryClientProvider client={new QueryClient()}>
        <AuthContext.Provider value={auth(role)}>
          <MemoryRouter>
            <DeviceCard device={makeDevice()} now={1_000_060} />
          </MemoryRouter>
        </AuthContext.Provider>
      </QueryClientProvider>,
    )

  it('is a link to the history page for admins', () => {
    card('admin')
    expect(
      screen.getByRole('link', { name: /Location history of Pixel/ }).getAttribute('href'),
    ).toBe('/devices/dradis/pixel/history')
  })
  it('is absent for viewers', () => {
    card('viewer')
    expect(screen.queryByRole('link', { name: /Location history/ })).toBeNull()
  })
})
