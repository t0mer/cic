// @vitest-environment jsdom
import { act, cleanup, fireEvent, render, screen } from '@testing-library/react'
import { StrictMode } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

type Fn = ReturnType<typeof vi.fn>
interface FakeMap {
  remove: Fn
  setStyle: Fn
  easeTo: Fn
  fitBounds: Fn
  flyTo: Fn
  emit: (event: string, payload?: unknown) => void
}
interface FakeMarker {
  remove: Fn
  setLngLat: Fn
  setOffset: Fn
}
const gl = vi.hoisted(() => ({
  maps: [] as FakeMap[],
  markers: [] as FakeMarker[],
  controls: [] as { options: Record<string, unknown> }[],
  throwOnCreate: false,
}))

vi.mock('maplibre-gl/dist/maplibre-gl.css', () => ({}))
vi.mock('maplibre-gl/dist/maplibre-gl-worker.mjs?url', () => ({ default: '/worker.mjs' }))
vi.mock('maplibre-gl', () => {
  class Map {
    remove = vi.fn()
    setStyle = vi.fn()
    handlers: Record<string, ((e: unknown) => void)[]> = {}
    on = vi.fn((event: string, cb: (e: unknown) => void) => {
      ;(this.handlers[event] ??= []).push(cb)
    })
    emit = (event: string, payload: unknown = {}) =>
      (this.handlers[event] ?? []).forEach((cb) => cb(payload))
    addControl = vi.fn()
    removeControl = vi.fn()
    fitBounds = vi.fn()
    easeTo = vi.fn()
    flyTo = vi.fn()
    getZoom = () => 3
    project = ([x, y]: [number, number]) => ({ x: x * 10, y: y * 10 })
    getContainer = () => ({ clientWidth: 375, clientHeight: 600 })
    touchZoomRotate = { disableRotation: vi.fn() }
    constructor() {
      if (gl.throwOnCreate) throw new Error('no webgl')
      gl.maps.push(this)
    }
  }
  class Marker {
    remove = vi.fn(() => this.element.remove())
    setLngLat = vi.fn(() => this)
    setOffset = vi.fn(() => this)
    element: HTMLElement
    constructor(o: { element: HTMLElement }) {
      this.element = o.element
      gl.markers.push(this)
    }
    addTo() {
      document.body.appendChild(this.element)
      return this
    }
    getElement() {
      return this.element
    }
  }
  class AttributionControl {
    constructor(public options: Record<string, unknown>) {
      gl.controls.push(this)
    }
  }
  return { Map, Marker, AttributionControl, setWorkerUrl: vi.fn() }
})

import { makeDevice } from '@/lib/fixtures'
import MapView from './MapView'

beforeEach(() => {
  gl.maps.length = 0
  gl.markers.length = 0
  gl.controls.length = 0
  gl.throwOnCreate = false
})
afterEach(cleanup)

const props = {
  styleUrl: 'https://x.test/light',
  attribution: 'A',
  geocodeEnabled: false,
  now: 1_000_060,
  selection: null,
}

describe('MapView', () => {
  it('creates one map, one marker per device, and cleans up on unmount', () => {
    const devices = [makeDevice(), makeDevice({ key: 'k/2', device: 'two', full_name: 'Two' })]
    const { rerender, unmount } = render(<MapView {...props} devices={devices} />)
    expect(gl.maps).toHaveLength(1)
    expect(gl.markers).toHaveLength(2)
    expect(screen.getByRole('button', { name: 'Fit all' })).toBeTruthy()
    // a live update must not recreate the map or the markers
    rerender(
      <MapView
        {...props}
        devices={[makeDevice({ battery_level: 5 }), devices[1]]}
        now={1_000_090}
      />,
    )
    expect(gl.maps).toHaveLength(1)
    expect(gl.markers).toHaveLength(2)
    expect(screen.getAllByTestId('low-battery-dot')).toHaveLength(1)
    unmount()
    expect(gl.maps[0].remove).toHaveBeenCalled()
    expect(gl.markers.every((m) => m.remove.mock.calls.length > 0)).toBe(true)
  })

  it('switches style on a theme change without recreating the map', () => {
    const { rerender } = render(<MapView {...props} devices={[makeDevice()]} />)
    rerender(<MapView {...props} devices={[makeDevice()]} styleUrl="https://x.test/dark" />)
    expect(gl.maps).toHaveLength(1)
    expect(gl.maps[0].setStyle).toHaveBeenCalledWith('https://x.test/dark')
  })

  it('shows a fallback message when the map cannot start', () => {
    gl.throwOnCreate = true
    render(<MapView {...props} devices={[makeDevice()]} />)
    expect(screen.getByRole('alert').textContent).toContain('could not start')
    expect(screen.getByRole('button', { name: 'Retry' })).toBeTruthy()
  })

  it('passes the server attribution to the control HTML-escaped', () => {
    render(
      <MapView
        {...props}
        attribution={'© <img src=x onerror=alert(1)> & "Co"'}
        devices={[makeDevice()]}
      />,
    )
    expect(gl.controls).toHaveLength(1)
    expect(gl.controls[0].options.customAttribution).toBe(
      '© &lt;img src=x onerror=alert(1)&gt; &amp; &quot;Co&quot;',
    )
  })

  it('shows a style error, and Retry reloads the style and clears the message', () => {
    render(<MapView {...props} devices={[makeDevice()]} />)
    const map = gl.maps[0]
    act(() => map.emit('error', { sourceId: 'tiles' })) // a tile error is not a style failure
    expect(screen.queryByRole('alert')).toBeNull()
    act(() => map.emit('error', {}))
    expect(screen.getByRole('alert').textContent).toContain('style failed to load')
    fireEvent.click(screen.getByRole('button', { name: 'Retry' }))
    expect(map.setStyle).toHaveBeenCalledWith('https://x.test/light')
    expect(screen.queryByRole('alert')).toBeNull()
  })

  it('fits the map again after a retry re-creates it', () => {
    gl.throwOnCreate = true
    render(<MapView {...props} devices={[makeDevice()]} />)
    expect(gl.maps).toHaveLength(0)
    gl.throwOnCreate = false
    fireEvent.click(screen.getByRole('button', { name: 'Retry' }))
    expect(gl.maps).toHaveLength(1)
    expect(gl.maps[0].easeTo).toHaveBeenCalledTimes(1)
  })

  it('fits the map that survives a StrictMode re-creation', () => {
    render(
      <StrictMode>
        <MapView {...props} devices={[makeDevice()]} />
      </StrictMode>,
    )
    const last = gl.maps[gl.maps.length - 1]
    expect(last.remove).not.toHaveBeenCalled()
    expect(last.easeTo).toHaveBeenCalledTimes(1)
  })

  it('flies to a selected device and opens its tooltip', () => {
    const d = makeDevice({ longitude: 34.5, latitude: 31.5 })
    const { rerender } = render(<MapView {...props} devices={[d]} />)
    rerender(<MapView {...props} devices={[d]} selection={{ key: d.key, nonce: 1 }} />)
    expect(gl.maps[0].flyTo).toHaveBeenCalledWith(expect.objectContaining({ center: [34.5, 31.5] }))
    expect(screen.getByTestId('map-tooltip')).toBeTruthy()
  })

  it('Esc closes the tooltip, returns focus to the marker and does not reopen it', () => {
    const real = Element.prototype.matches
    vi.spyOn(Element.prototype, 'matches').mockImplementation(function (this: Element, q: string) {
      return q === ':focus-visible' ? true : real.call(this, q)
    })
    const d = makeDevice()
    const { rerender } = render(<MapView {...props} devices={[d]} />)
    rerender(<MapView {...props} devices={[d]} selection={{ key: d.key, nonce: 1 }} />)
    const link = screen.getByRole('link', { name: /Open in Google Maps/ })
    act(() => link.focus())
    fireEvent.keyDown(document, { key: 'Escape' })
    expect(screen.queryByTestId('map-tooltip')).toBeNull()
    const btn = document.querySelector('[data-marker-btn]')
    expect(document.activeElement).toBe(btn)
    expect(btn?.getAttribute('aria-expanded')).toBe('false')
  })

  it('does not reposition markers for an unrelated live update', () => {
    const a = makeDevice({ key: 'k/a', device: 'a', longitude: 1, latitude: 1 })
    const b = makeDevice({ key: 'k/b', device: 'b', longitude: 1.001, latitude: 1.001 })
    const { rerender } = render(<MapView {...props} devices={[a, b]} />)
    const calls = () =>
      gl.markers.map((m) => [m.setLngLat.mock.calls.length, m.setOffset.mock.calls.length])
    const before = calls()
    rerender(<MapView {...props} devices={[{ ...a }, { ...b, battery_level: 3 }]} />)
    expect(calls()).toEqual(before)
    // control: a real move does reposition
    rerender(<MapView {...props} devices={[{ ...a, longitude: 9 }, b]} />)
    expect(gl.markers[0].setLngLat.mock.calls.length).toBeGreaterThan(before[0][0])
  })
})
