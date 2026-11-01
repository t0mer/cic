// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { makeDevice } from '@/lib/fixtures'
import { listDevices, MapDeviceList } from './MapDeviceList'

afterEach(cleanup)

const devs = [
  makeDevice({ key: 'k/c', device: 'c', full_name: 'Charlie', location_name: 'Haifa' }),
  makeDevice({ key: 'k/a', device: 'a', full_name: 'alpha', location_name: 'Tel Aviv' }),
  makeDevice({ key: 'k/b', device: 'b', full_name: 'Bravo', latitude: null, longitude: null }),
  makeDevice({ key: 'k/h', device: 'h', full_name: 'Hidden', hidden: true }),
]

describe('listDevices', () => {
  it('sorts by name case-insensitively and drops hidden devices', () => {
    expect(listDevices(devs, '').map((d) => d.device)).toEqual(['a', 'b', 'c'])
  })
  it('filters by name, device id and location name', () => {
    expect(listDevices(devs, 'brav').map((d) => d.device)).toEqual(['b'])
    expect(listDevices(devs, 'haifa').map((d) => d.device)).toEqual(['c'])
    expect(listDevices(devs, 'zzz')).toEqual([])
  })
})

describe('MapDeviceList', () => {
  it('lists devices, marks the ones without a location and selects the others', () => {
    const onSelect = vi.fn()
    render(<MapDeviceList devices={devs} query="" onQuery={() => {}} onSelect={onSelect} />)
    expect(screen.getByText('No location')).toBeTruthy()
    expect(screen.queryByRole('button', { name: /Bravo/ })).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'Show Charlie on the map' }))
    expect(onSelect).toHaveBeenCalledWith('k/c')
  })
  it('reports search input', () => {
    const onQuery = vi.fn()
    render(<MapDeviceList devices={devs} query="" onQuery={onQuery} onSelect={() => {}} />)
    fireEvent.change(screen.getByRole('searchbox'), { target: { value: 'x' } })
    expect(onQuery).toHaveBeenCalledWith('x')
  })
})

describe('MapDeviceList place text', () => {
  const place = (d: ReturnType<typeof makeDevice>) => {
    render(<MapDeviceList devices={[d]} query="" onQuery={() => {}} onSelect={() => {}} />)
    return screen.getByRole('listitem').textContent ?? ''
  }
  it('shows the coordinates, never "unavailable", when there is no location name', () => {
    const text = place(makeDevice({ location_name: null }))
    expect(text).toContain('32.0853, 34.7818')
    expect(text).not.toMatch(/unavailable/i)
  })
  it('shows the name when there is one', () => {
    expect(place(makeDevice({ location_name: 'Haifa' }))).toContain('Haifa')
  })
})
