// @vitest-environment jsdom
import { cleanup, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { SettingsPage, visibleTabs } from './SettingsPage'
import { renderWith, stubApi } from './test-utils'

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

describe('role-based rendering', () => {
  it('viewers see only Account and fire no admin requests', async () => {
    const calls = stubApi(() => ({ json: {} }))
    renderWith(<SettingsPage />, 'viewer')
    const tabs = screen.getAllByRole('tab')
    expect(tabs.map((t) => t.textContent)).toEqual(['Account'])
    expect(screen.getByRole('heading', { name: 'Account' })).toBeTruthy()
    await new Promise((r) => setTimeout(r, 50))
    expect(calls).toEqual([])
    expect(visibleTabs(false).map((t) => t.id)).toEqual(['account'])
  })

  it('a viewer cannot reach an admin tab through the URL', () => {
    stubApi(() => ({ json: [] }))
    renderWith(<SettingsPage />, 'viewer')
    expect(screen.queryByRole('heading', { name: 'Users' })).toBeNull()
  })

  it('admins get all six tabs and only the active one loads data', async () => {
    const calls = stubApi((c) => (c.url.startsWith('/api/devices') ? { json: [] } : { json: {} }))
    renderWith(<SettingsPage />, 'admin')
    expect(screen.getAllByRole('tab').map((t) => t.textContent)).toEqual([
      'Devices',
      'Users',
      'Safe places',
      'Geocoding',
      'System',
      'Account',
    ])
    await waitFor(() => expect(calls.length).toBeGreaterThan(0))
    expect(calls.every((c) => c.url === '/api/devices?include_hidden=1')).toBe(true)
  })
})
