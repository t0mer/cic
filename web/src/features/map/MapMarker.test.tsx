// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

vi.mock('maplibre-gl', () => {
  class Marker {
    element: HTMLElement
    constructor(o: { element: HTMLElement }) {
      this.element = o.element
    }
    addTo() {
      document.body.appendChild(this.element)
      return this
    }
    remove() {
      this.element.remove()
    }
    setLngLat() {
      return this
    }
    setOffset() {
      return this
    }
    getElement() {
      return this.element
    }
  }
  return { Marker }
})

import type { Map as GLMap } from 'maplibre-gl'

import { makeDevice } from '@/lib/fixtures'
import { MapMarker } from './MapMarker'
import type { useHoverIntent } from './useHoverIntent'

const device = makeDevice()
const GMAPS = /google\.com\/maps/

function fakeMap(height = 300): GLMap {
  return {
    project: () => ({ x: 100, y: 100 }),
    getContainer: () => ({ clientWidth: 400, clientHeight: height }),
  } as unknown as GLMap
}

function fakeHover() {
  return {
    activeKey: null,
    enter: vi.fn(),
    leave: vi.fn(),
    show: vi.fn(),
    hide: vi.fn(),
    release: vi.fn(),
    focus: vi.fn(),
    suppressFocus: vi.fn(),
    setHold: vi.fn(),
  } satisfies ReturnType<typeof useHoverIntent>
}

function mount(opts: { canHover?: boolean; active?: boolean; height?: number } = {}) {
  const hover = fakeHover()
  render(
    <MapMarker
      map={fakeMap(opts.height)}
      device={device}
      offset={[0, 0]}
      tick={0}
      active={opts.active ?? false}
      canHover={opts.canHover ?? true}
      now={1_000_060}
      geocodeEnabled={false}
      hover={hover}
    />,
  )
  return { hover, btn: document.querySelector('[data-marker-btn]') as HTMLElement }
}

let open: ReturnType<typeof vi.fn>
beforeEach(() => {
  open = vi.fn()
  vi.stubGlobal('open', open)
})
afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

describe('MapMarker click behaviour by input type', () => {
  it('mouse on a hover-capable device opens Google Maps', () => {
    const { hover, btn } = mount()
    fireEvent.pointerDown(btn, { pointerType: 'mouse' })
    fireEvent.click(btn, { detail: 1 })
    expect(open).toHaveBeenCalledWith(expect.stringMatching(GMAPS), '_blank', 'noopener,noreferrer')
    expect(hover.show).not.toHaveBeenCalled()
  })

  it('a pen on a hover-capable device opens Google Maps', () => {
    const { btn } = mount()
    fireEvent.pointerDown(btn, { pointerType: 'pen' })
    fireEvent.click(btn, { detail: 1 })
    expect(open).toHaveBeenCalledTimes(1)
  })

  it('touch toggles the tooltip even when the device also reports hover (iPad + trackpad)', () => {
    const { hover, btn } = mount({ canHover: true })
    fireEvent.pointerDown(btn, { pointerType: 'touch' })
    fireEvent.click(btn, { detail: 1 })
    expect(open).not.toHaveBeenCalled()
    expect(hover.show).toHaveBeenCalledWith(device.key)
  })

  it('touch closes an open tooltip', () => {
    const { hover, btn } = mount({ active: true })
    fireEvent.pointerDown(btn, { pointerType: 'touch' })
    fireEvent.click(btn, { detail: 1 })
    expect(hover.hide).toHaveBeenCalled()
    expect(open).not.toHaveBeenCalled()
  })

  it('mouse on a device without hover toggles', () => {
    const { hover, btn } = mount({ canHover: false })
    fireEvent.pointerDown(btn, { pointerType: 'mouse' })
    fireEvent.click(btn, { detail: 1 })
    expect(open).not.toHaveBeenCalled()
    expect(hover.show).toHaveBeenCalled()
  })

  it('an unknown pointer toggles', () => {
    const { hover, btn } = mount()
    fireEvent.click(btn, { detail: 1 })
    expect(open).not.toHaveBeenCalled()
    expect(hover.show).toHaveBeenCalled()
  })

  it('keyboard Enter opens Google Maps, Space toggles', () => {
    const { hover, btn } = mount()
    fireEvent.keyDown(btn, { key: 'Enter' })
    fireEvent.click(btn, { detail: 0 })
    expect(open).toHaveBeenCalledTimes(1)
    fireEvent.keyDown(btn, { key: ' ' })
    fireEvent.click(btn, { detail: 0 })
    expect(open).toHaveBeenCalledTimes(1)
    expect(hover.show).toHaveBeenCalledTimes(1)
  })

  it('a pointer click after an Enter press does not reuse the Enter', () => {
    const { hover, btn } = mount()
    fireEvent.keyDown(btn, { key: 'Enter' })
    fireEvent.click(btn, { detail: 0 })
    fireEvent.pointerDown(btn, { pointerType: 'touch' })
    fireEvent.click(btn, { detail: 1 })
    expect(open).toHaveBeenCalledTimes(1)
    expect(hover.show).toHaveBeenCalledTimes(1)
  })
})

describe('MapMarker tooltip sizing', () => {
  const scrollHeight = Object.getOwnPropertyDescriptor(HTMLElement.prototype, 'scrollHeight')
  afterEach(() => {
    if (scrollHeight) Object.defineProperty(HTMLElement.prototype, 'scrollHeight', scrollHeight)
  })
  const measured = (h: number) =>
    Object.defineProperty(HTMLElement.prototype, 'scrollHeight', {
      configurable: true,
      get: () => h,
    })

  it('caps a tooltip that fits on neither side and lets it scroll', () => {
    measured(400)
    mount({ active: true, height: 300 })
    const tip = screen.getByTestId('map-tooltip')
    expect(tip.className).toContain('overflow-auto')
    expect(tip.style.maxHeight).toBe('164px') // roomier (bottom) side: 300 - 100 - 28 - 8
  })

  it('uses the measured height to choose the top when the tooltip is short', () => {
    measured(50)
    mount({ active: true, height: 300 })
    expect(screen.getByTestId('map-tooltip').parentElement?.className).toContain('bottom-full')
  })

  it('goes below when the measured tooltip does not fit above but fits below', () => {
    measured(120)
    mount({ active: true, height: 300 })
    expect(screen.getByTestId('map-tooltip').parentElement?.className).toContain('top-full')
  })
})

describe('MapMarker unmount', () => {
  it('releases its tooltip from the shared hover state', () => {
    const hover = fakeHover()
    const { unmount } = render(
      <MapMarker
        map={fakeMap()}
        device={device}
        offset={[0, 0]}
        tick={0}
        active={false}
        canHover
        now={1_000_060}
        geocodeEnabled={false}
        hover={hover}
      />,
    )
    unmount()
    expect(hover.release).toHaveBeenCalledWith(device.key)
  })
})
