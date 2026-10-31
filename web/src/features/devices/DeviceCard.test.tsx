// @vitest-environment jsdom
import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'

import { makeDevice } from '@/lib/fixtures'
import { DeviceCard } from './DeviceCard'

afterEach(cleanup)
const NOW = 1_000_060

describe('DeviceCard', () => {
  it('shows an online device', () => {
    render(<DeviceCard device={makeDevice({ charging: true, charge_type: 'USB' })} now={NOW} />)
    expect(screen.getByText('Online')).toBeTruthy()
    expect(screen.getByText('Last seen 1 min ago')).toBeTruthy()
    expect(screen.getByText('32.0853, 34.7818')).toBeTruthy()
    expect(screen.getByText('80%')).toBeTruthy()
    expect(screen.getByText(/Charging · USB/)).toBeTruthy()
    expect(screen.getByText('Home')).toBeTruthy()
    expect(screen.getByText('v2026.9.1')).toBeTruthy()
    const link = screen.getByRole('link', { name: /Google Maps/ })
    expect(link.getAttribute('href')).toBe(
      'https://www.google.com/maps/search/?api=1&query=32.08531,34.78176',
    )
    expect(link.getAttribute('rel')).toBe('noopener noreferrer')
    expect(link.getAttribute('target')).toBe('_blank')
  })

  it('shows an offline device', () => {
    render(<DeviceCard device={makeDevice({ online: false })} now={NOW} />)
    expect(screen.getByText('Offline')).toBeTruthy()
  })

  it('shows never seen and unknown location', () => {
    render(
      <DeviceCard
        device={makeDevice({ online: false, last_seen: 0, latitude: null, longitude: null })}
        now={NOW}
      />,
    )
    expect(screen.getByText('Never seen')).toBeTruthy()
    expect(screen.getByText('Location unknown')).toBeTruthy()
    expect(screen.queryByRole('link')).toBeNull()
  })

  it('shows unknown battery', () => {
    render(<DeviceCard device={makeDevice({ battery_level: null })} now={NOW} />)
    expect(screen.getByText('Battery unknown')).toBeTruthy()
  })

  it('renders a Hebrew name with its first letter and dir=auto', () => {
    render(<DeviceCard device={makeDevice({ full_name: 'תומר' })} now={NOW} />)
    const h = screen.getByRole('heading', { name: 'תומר' })
    expect(h.getAttribute('dir')).toBe('auto')
    expect(screen.getByText('ת')).toBeTruthy()
  })

  it('shows Mobile data when Wi-Fi is off', () => {
    render(<DeviceCard device={makeDevice({ wifi_connected: false, ssid: null })} now={NOW} />)
    expect(screen.getByText('Mobile data')).toBeTruthy()
  })

  it('warns on low battery', () => {
    render(<DeviceCard device={makeDevice({ battery_level: 15 })} now={NOW} />)
    expect(screen.getByText('15%')).toBeTruthy()
    expect(screen.getByText('Low battery')).toBeTruthy()
  })

  it('does not flag a healthy battery as low', () => {
    render(<DeviceCard device={makeDevice({ battery_level: 80 })} now={NOW} />)
    expect(screen.queryByText('Low battery')).toBeNull()
  })

  it('trusts the server online flag even when last_seen is old', () => {
    render(<DeviceCard device={makeDevice({ online: true, last_seen: 1 })} now={NOW} />)
    expect(screen.getByText('Online')).toBeTruthy()
    expect(screen.queryByText('Offline')).toBeNull()
  })
})
