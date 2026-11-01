// @vitest-environment jsdom
import { cleanup, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import type { GeocodeUsage } from '@/lib/types'
import { GeocodingSection } from './GeocodingSection'
import { renderWith, stubApi } from './test-utils'

vi.mock('sonner', () => ({ toast: { error: vi.fn(), success: vi.fn(), warning: vi.fn() } }))

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

const cfg = (geocode_enabled: boolean) => ({
  map: {
    provider: 'x',
    style_light: 'https://a.test/l',
    style_dark: 'https://a.test/d',
    attribution: '',
  },
  geocode_enabled,
})
const usage = (over: Partial<GeocodeUsage> = {}): GeocodeUsage => ({
  enabled: true,
  calls_today: 25,
  cache_hits_today: 75,
  remaining: 75,
  daily_cap: 100,
  blocked_until: null,
  cache_hit_ratio: 0.75,
  ...over,
})

describe('GeocodingSection', () => {
  it('shows the off state without asking for usage', async () => {
    const calls = stubApi((c) => (c.url === '/api/ui-config' ? { json: cfg(false) } : { json: {} }))
    renderWith(<GeocodingSection />)
    await screen.findByText('Geocoding is off')
    expect(screen.getByText(/server configuration/)).toBeTruthy()
    expect(calls.map((c) => c.url)).toEqual(['/api/ui-config'])
  })

  it('shows "not available" gracefully when usage 404s, with no error toast', async () => {
    const { toast } = await import('sonner')
    stubApi((c) =>
      c.url === '/api/ui-config'
        ? { json: cfg(true) }
        : { status: 404, json: { error: 'not found' } },
    )
    renderWith(<GeocodingSection />)
    await screen.findByText('Geocoding not available')
    expect(screen.queryByRole('alert')).toBeNull()
    expect(vi.mocked(toast.error)).not.toHaveBeenCalled()
  })

  it('renders the meter, ratio and remaining quota', async () => {
    stubApi((c) => (c.url === '/api/ui-config' ? { json: cfg(true) } : { json: usage() }))
    renderWith(<GeocodingSection />)
    const meter = await screen.findByRole('meter')
    expect(meter.getAttribute('aria-valuenow')).toBe('25')
    expect(meter.getAttribute('aria-valuemax')).toBe('100')
    expect(screen.getByText('75%')).toBeTruthy()
    expect(screen.getAllByText('75').length).toBeGreaterThan(0)
    expect(screen.getByText(/free tier is shared/)).toBeTruthy()
  })

  it('warns when lookups are blocked and handles an unknown remaining quota', async () => {
    const until = new Date(Date.now() + 3_600_000).toISOString()
    stubApi((c) =>
      c.url === '/api/ui-config'
        ? { json: cfg(true) }
        : { json: usage({ blocked_until: until, remaining: null }) },
    )
    renderWith(<GeocodingSection />)
    expect((await screen.findByRole('alert')).textContent).toMatch(/paused until/)
    expect(screen.getByText('Unknown')).toBeTruthy()
  })

  it('reports an unexpected failure inline', async () => {
    stubApi((c) =>
      c.url === '/api/ui-config' ? { json: cfg(true) } : { status: 500, json: { error: 'boom' } },
    )
    renderWith(<GeocodingSection />)
    expect((await screen.findByRole('alert')).textContent).toMatch(/boom/)
  })
})
