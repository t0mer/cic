// @vitest-environment jsdom
import { cleanup, fireEvent, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { makeDevice } from '@/lib/fixtures'
import type { Place } from '@/lib/types'
import {
  latitudeProblem,
  longitudeProblem,
  placeNameProblem,
  radiusProblem,
} from '@/lib/validation'
import { placeBody } from './PlaceDialog'
import { PlacesSection } from './PlacesSection'
import { renderWith, stubApi } from './test-utils'

vi.mock('sonner', () => ({ toast: { error: vi.fn(), success: vi.fn(), warning: vi.fn() } }))

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

const click = (el: HTMLElement) => fireEvent.click(el)
const type = (el: HTMLElement, value: string) => fireEvent.change(el, { target: { value } })

const home: Place = { id: 1, name: 'Home', latitude: 32.0853, longitude: 34.7818, radius_m: 150 }

describe('place validation', () => {
  it('name: 1 to 40 characters', () => {
    expect(placeNameProblem('Home')).toBeNull()
    expect(placeNameProblem('   ')).toMatch(/required/)
    expect(placeNameProblem('x'.repeat(40))).toBeNull()
    expect(placeNameProblem('x'.repeat(41))).toMatch(/40/)
    expect(placeNameProblem('a\u0000b')).not.toBeNull()
  })
  it('latitude and longitude ranges', () => {
    expect(latitudeProblem('90')).toBeNull()
    expect(latitudeProblem('-90.1')).not.toBeNull()
    expect(latitudeProblem('')).not.toBeNull()
    expect(latitudeProblem('abc')).not.toBeNull()
    expect(longitudeProblem('180')).toBeNull()
    expect(longitudeProblem('180.5')).not.toBeNull()
  })
  it('radius 10 to 50000', () => {
    expect(radiusProblem('10')).toBeNull()
    expect(radiusProblem('50000')).toBeNull()
    expect(radiusProblem('9')).not.toBeNull()
    expect(radiusProblem('50001')).not.toBeNull()
    expect(radiusProblem('')).not.toBeNull()
    expect(radiusProblem('100.5')).toBe('Radius must be a whole number of metres')
  })
})

describe('placeBody', () => {
  const f = { name: ' Home ', latitude: '32.0853', longitude: '34.7818', radius: '150' }
  it('sends everything for a new place', () => {
    expect(placeBody(null, f)).toEqual({
      name: 'Home',
      latitude: 32.0853,
      longitude: 34.7818,
      radius_m: 150,
    })
  })
  it('sends only the changed fields when editing', () => {
    expect(placeBody(home, f)).toEqual({})
    expect(placeBody(home, { ...f, radius: '200' })).toEqual({ radius_m: 200 })
  })
})

describe('PlacesSection', () => {
  it('lists places, with the add button', async () => {
    stubApi(() => ({ json: [home] }))
    renderWith(<PlacesSection />)
    expect(await screen.findByText('Home')).toBeTruthy()
    expect(screen.getByText(/150 m radius/)).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Add place' })).toBeTruthy()
  })

  it('accepts a {places: []} envelope', async () => {
    stubApi(() => ({ json: { places: [home] } }))
    renderWith(<PlacesSection />)
    expect(await screen.findByText('Home')).toBeTruthy()
  })

  it('adds a place from a phone location and reloads places and devices', async () => {
    const pixel = makeDevice({ full_name: 'Pixel', latitude: 1.5, longitude: 2.5 })
    const calls = stubApi((c) => {
      if (c.url === '/api/places' && c.method === 'POST')
        return { status: 201, json: { id: 2, ...(c.body as object) } }
      if (c.url.startsWith('/api/devices')) return { json: [pixel] }
      return { json: [] }
    })
    renderWith(<PlacesSection />)
    await screen.findByText('No safe places yet')
    click(screen.getByRole('button', { name: 'Add place' }))
    type(screen.getByLabelText('Name'), 'Work')
    const select = screen.getByLabelText(/Use a phone/) as HTMLSelectElement
    await waitFor(() => expect(select.disabled).toBe(false))
    fireEvent.change(select, { target: { value: pixel.key } })
    expect((screen.getByLabelText('Latitude') as HTMLInputElement).value).toBe('1.5')
    click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() =>
      expect(calls.find((c) => c.method === 'POST')?.body).toEqual({
        name: 'Work',
        latitude: 1.5,
        longitude: 2.5,
        radius_m: 100,
      }),
    )
    await waitFor(() =>
      expect(calls.filter((c) => c.url === '/api/places' && c.method === 'GET').length).toBe(2),
    )
  })

  it('blocks an invalid form and sends nothing', async () => {
    const calls = stubApi(() => ({ json: [] }))
    renderWith(<PlacesSection />)
    await screen.findByText('No safe places yet')
    click(screen.getByRole('button', { name: 'Add place' }))
    click(screen.getByRole('button', { name: 'Save' }))
    expect(screen.getByText('Name is required')).toBeTruthy()
    expect(screen.getByText(/Latitude must be/)).toBeTruthy()
    expect(calls.some((c) => c.method === 'POST')).toBe(false)
  })

  it('edits with a partial patch and shows a friendly duplicate-name error', async () => {
    const calls = stubApi((c) =>
      c.method === 'PATCH'
        ? { status: 409, json: { error: 'conflict', code: 'duplicate_name' } }
        : { json: c.url.startsWith('/api/places') ? [home] : [] },
    )
    renderWith(<PlacesSection />)
    click(await screen.findByRole('button', { name: 'Edit Home' }))
    const name = screen.getByLabelText('Name')
    type(name, 'Work')
    click(screen.getByRole('button', { name: 'Save' }))
    expect((await screen.findByRole('alert')).textContent).toMatch(/already exists/)
    expect(calls.find((c) => c.method === 'PATCH')).toMatchObject({
      url: '/api/places/1',
      body: { name: 'Work' },
    })
  })

  it('deletes after confirmation', async () => {
    const calls = stubApi((c) =>
      c.method === 'DELETE' ? { status: 204 } : { json: c.url === '/api/places' ? [home] : [] },
    )
    renderWith(<PlacesSection />)
    click(await screen.findByRole('button', { name: 'Delete Home' }))
    expect(calls.some((c) => c.method === 'DELETE')).toBe(false)
    fireEvent.click(screen.getByRole('button', { name: 'Delete', hidden: false }))
    await waitFor(() => expect(calls.find((c) => c.method === 'DELETE')?.url).toBe('/api/places/1'))
  })
})
