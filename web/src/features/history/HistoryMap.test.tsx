// @vitest-environment jsdom
import { act, cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

type Fn = ReturnType<typeof vi.fn>
interface FakeMap {
  remove: Fn
  setStyle: Fn
  fitBounds: Fn
  removeControl: Fn
  sources: Record<string, { setData: Fn }>
  layers: string[]
  emit: (event: string, layer?: string, payload?: unknown) => void
}
const gl = vi.hoisted(() => ({ maps: [] as FakeMap[], throwOnCreate: false }))

vi.mock('maplibre-gl/dist/maplibre-gl.css', () => ({}))
vi.mock('maplibre-gl/dist/maplibre-gl-worker.mjs?url', () => ({ default: '/worker.mjs' }))
vi.mock('maplibre-gl', () => {
  class Map {
    remove = vi.fn()
    setStyle = vi.fn()
    fitBounds = vi.fn()
    easeTo = vi.fn()
    addControl = vi.fn()
    removeControl = vi.fn()
    sources: Record<string, { setData: Fn }> = {}
    layers: string[] = []
    handlers: Record<string, ((e: unknown) => void)[]> = {}
    touchZoomRotate = { disableRotation: vi.fn() }
    getCanvas = () => ({ style: {} })
    getSource = (id: string) => this.sources[id]
    addSource = (id: string) => {
      this.sources[id] = { setData: vi.fn() }
    }
    getLayer = (id: string) => (this.layers.includes(id) ? {} : undefined)
    addLayer = (l: { id: string }) => this.layers.push(l.id)
    on = vi.fn((event: string, a: unknown, b?: unknown) => {
      const key = typeof a === 'string' ? `${event}:${a}` : event
      ;(this.handlers[key] ??= []).push((typeof a === 'string' ? b : a) as (e: unknown) => void)
    })
    emit = (event: string, layer?: string, payload: unknown = {}) =>
      (this.handlers[layer ? `${event}:${layer}` : event] ?? []).forEach((cb) => cb(payload))
    constructor() {
      if (gl.throwOnCreate) throw new Error('no webgl')
      gl.maps.push(this as unknown as FakeMap)
    }
  }
  class AttributionControl {}
  return { Map, AttributionControl, setWorkerUrl: vi.fn() }
})

import { groupDays } from '@/lib/history-days'
import HistoryMap from './HistoryMap'

beforeEach(() => {
  gl.maps.length = 0
  gl.throwOnCreate = false
})
afterEach(cleanup)

const days = groupDays([
  {
    date: '2026-10-01',
    distance_m: 100,
    point_count: 2,
    first: 1_760_000_000,
    last: 1_760_000_100,
    segments: [
      [
        [32, 34, 1_760_000_000],
        [32.01, 34.01, 1_760_000_100],
      ],
    ],
  },
])
const props = {
  styleUrl: 'https://x.test/light',
  attribution: 'A',
  accuracyByT: new Map([[1_760_000_100, 12.4]]),
  fitKey: 'k1',
}

describe('HistoryMap', () => {
  it('draws after the style loads, fits once, and cleans up without touching a removed map', () => {
    const { rerender, unmount } = render(<HistoryMap {...props} days={days} />)
    const m = gl.maps[0]
    expect(m.layers).toEqual([])
    act(() => m.emit('style.load'))
    expect(m.layers).toEqual(['history-casing', 'history-line', 'history-points', 'history-ends'])
    expect(m.fitBounds).toHaveBeenCalledTimes(1)
    // a visibility toggle updates the data but must not move the camera
    rerender(<HistoryMap {...props} days={[]} />)
    expect(m.sources['history-lines'].setData).toHaveBeenCalled()
    expect(m.fitBounds).toHaveBeenCalledTimes(1)
    // a new range re-fits
    rerender(<HistoryMap {...props} days={days} fitKey="k2" />)
    expect(m.fitBounds).toHaveBeenCalledTimes(2)
    unmount()
    expect(m.remove).toHaveBeenCalled()
    expect(m.removeControl).not.toHaveBeenCalled()
  })

  it('re-adds the layers after a theme change and shows time and accuracy of a clicked point', () => {
    const { rerender } = render(<HistoryMap {...props} days={days} />)
    const m = gl.maps[0]
    act(() => m.emit('style.load'))
    rerender(<HistoryMap {...props} days={days} styleUrl="https://x.test/dark" />)
    expect(m.setStyle).toHaveBeenCalledWith('https://x.test/dark')
    act(() =>
      m.emit('click', 'history-points', {
        features: [
          {
            geometry: { type: 'Point', coordinates: [34.01, 32.01] },
            properties: { t: 1_760_000_100 },
          },
        ],
      }),
    )
    const info = screen.getByTestId('history-point-info')
    expect(info.textContent).toContain('±12 m')
    expect(info.textContent).toContain('32.01000, 34.01000')
    fireEvent.click(screen.getByRole('button', { name: 'Close point details' }))
    expect(screen.queryByTestId('history-point-info')).toBeNull()
  })

  it('offers a Fit route button and reports a map that cannot start', () => {
    render(<HistoryMap {...props} days={days} />)
    act(() => gl.maps[0].emit('style.load'))
    fireEvent.click(screen.getByRole('button', { name: 'Fit route' }))
    expect(gl.maps[0].fitBounds).toHaveBeenCalledTimes(2)
    cleanup()
    gl.throwOnCreate = true
    render(<HistoryMap {...props} days={days} />)
    expect(screen.getByRole('alert').textContent).toMatch(/map could not start/)
  })
})
