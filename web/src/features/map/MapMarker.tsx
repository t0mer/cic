import { Marker, type Map as GLMap } from 'maplibre-gl'
import { useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
import { createPortal } from 'react-dom'

import { placeTooltip, type Offset } from '@/lib/cluster'
import { colorForKey, readableText } from '@/lib/color'
import { firstGrapheme } from '@/lib/grapheme'
import { describeLocation } from '@/lib/location'
import { googleMapsUrl } from '@/lib/maps'
import type { Device } from '@/lib/types'
import { cn } from '@/lib/utils'
import { MapTooltip } from './MapTooltip'
import type { useHoverIntent } from './useHoverIntent'

export const LOW_BATTERY = 20
const TIP_W = 256
/** Used until the real tooltip has been measured (first paint only). */
const TIP_H_GUESS = 230

export function markerLabel(d: Device, geocodeEnabled: boolean, now: number): string {
  const name = d.full_name || d.device
  const view = describeLocation(d, geocodeEnabled, now)
  const place = view.kind === 'named' ? view.name : view.kind === 'unknown' ? '' : view.coords
  return `${name}, ${d.online ? 'online' : 'offline'}${place ? `, ${place}` : ''}`
}

/**
 * One DOM marker: a maplibre Marker whose element hosts a React portal (avatar button + tooltip),
 * so live query-cache updates re-render it without ever recreating the map or the marker.
 */
export function MapMarker({
  map,
  device,
  offset,
  tick,
  active,
  canHover,
  now,
  geocodeEnabled,
  hover,
}: {
  map: GLMap
  device: Device
  offset: Offset
  /** Bumped by the map on zoom/move end so the tooltip placement is recomputed. */
  tick: number
  active: boolean
  canHover: boolean
  now: number
  geocodeEnabled: boolean
  hover: ReturnType<typeof useHoverIntent>
}) {
  const key = device.key
  const lat = device.latitude as number
  const lng = device.longitude as number
  const [el] = useState(() => {
    const e = document.createElement('div')
    e.dataset.marker = key
    return e
  })
  const [marker] = useState(() => new Marker({ element: el, offset: offset }).setLngLat([lng, lat]))

  useEffect(() => {
    marker.addTo(map)
    return () => {
      marker.remove()
    }
  }, [marker, map])
  useEffect(() => {
    marker.setLngLat([lng, lat]).setOffset(offset)
  }, [marker, lng, lat, offset])
  useEffect(() => {
    marker.getElement().style.setProperty('z-index', active ? '5' : '')
  }, [marker, active])

  // Measure the real tooltip (its content varies: long names, Ping/Stop, wrapped buttons).
  const [tipEl, setTipEl] = useState<HTMLElement | null>(null)
  const [tipH, setTipH] = useState(0)
  useLayoutEffect(() => {
    if (!tipEl) return
    // scrollHeight is the natural content height, so a capped (scrolling) tooltip is not
    // mistaken for a short one and placement cannot oscillate.
    const measure = () => setTipH(tipEl.scrollHeight)
    measure()
    if (typeof ResizeObserver === 'undefined') return
    const ro = new ResizeObserver(measure)
    ro.observe(tipEl)
    if (tipEl.firstElementChild) ro.observe(tipEl.firstElementChild)
    return () => ro.disconnect()
  }, [tipEl])

  // The open tooltip must not outlive its marker (or keep a hold on the shared hover state).
  const { release } = hover
  useEffect(() => () => release(key), [release, key])

  const placement = useMemo(() => {
    if (!active) return null
    void tick
    const p = map.project([lng, lat])
    const c = map.getContainer()
    return placeTooltip(
      { x: p.x + offset[0], y: p.y + offset[1] },
      { width: c.clientWidth, height: c.clientHeight },
      { width: TIP_W, height: tipH || TIP_H_GUESS },
    )
  }, [active, map, lng, lat, offset, tick, tipH])

  const name = device.full_name || device.device
  const bg = colorForKey(device.key, device.color)
  const low = device.battery_level !== null && device.battery_level < LOW_BATTERY

  // What last touched the marker: decides whether a click opens Google Maps or toggles the
  // tooltip. `canHover` alone is wrong on hybrids (iPad + trackpad, touch laptops).
  const pointerType = useRef('')
  const enterKey = useRef(false)
  const inside = useRef(false)
  const onClick = (e: React.MouseEvent) => {
    const keyboard = e.detail === 0
    const type = pointerType.current
    const enter = enterKey.current
    enterKey.current = false
    if (keyboard) {
      // Enter keeps the documented behaviour (open Google Maps); Space / programmatic clicks toggle.
      if (enter) window.open(googleMapsUrl(lat, lng), '_blank', 'noopener,noreferrer')
      else if (active) hover.hide()
      else hover.show(key)
      return
    }
    if (canHover && (type === 'mouse' || type === 'pen')) {
      window.open(googleMapsUrl(lat, lng), '_blank', 'noopener,noreferrer')
    } else if (active) hover.hide()
    else hover.show(key)
  }
  const keyboardFocus = (t: HTMLElement) => {
    try {
      return t.matches(':focus-visible')
    } catch {
      return true
    }
  }

  return createPortal(
    <div
      className="relative size-11"
      onPointerDownCapture={(e) => {
        pointerType.current = e.pointerType
      }}
      onPointerEnter={(e) => {
        pointerType.current = e.pointerType
        if (e.pointerType !== 'touch') {
          inside.current = true
          hover.enter(key)
        }
      }}
      onPointerLeave={(e) => {
        if (e.pointerType !== 'touch') {
          inside.current = false
          hover.leave()
        }
      }}
      onFocus={(e) => {
        const t = e.target as HTMLElement
        if (t.dataset.markerBtn === undefined || keyboardFocus(t)) hover.focus(key)
      }}
      onBlur={(e) => {
        if (!e.currentTarget.contains(e.relatedTarget as Node | null)) hover.leave()
      }}
    >
      <button
        type="button"
        data-marker-btn=""
        data-online={device.online}
        aria-label={markerLabel(device, geocodeEnabled, now)}
        aria-expanded={active}
        onClick={onClick}
        onKeyDown={(e) => {
          enterKey.current = e.key === 'Enter'
        }}
        className="relative flex size-11 items-center justify-center rounded-full outline-none focus-visible:ring-[3px] focus-visible:ring-ring"
      >
        <span
          aria-hidden="true"
          className={cn(
            'flex size-10 items-center justify-center rounded-full border-[3px] text-base font-semibold shadow-md ring-2 ring-white dark:ring-zinc-900',
            device.online ? 'border-green-600 dark:border-green-400' : 'border-zinc-500',
          )}
          style={{ backgroundColor: bg, color: readableText(bg) }}
        >
          {firstGrapheme(name)}
        </span>
        {low ? (
          <span
            aria-hidden="true"
            data-testid="low-battery-dot"
            className="absolute end-0 top-0 size-3.5 rounded-full border-2 border-white bg-amber-500 dark:border-zinc-900"
          />
        ) : null}
      </button>
      {active && placement ? (
        <div
          className={cn(
            'absolute inset-x-0 z-10 flex justify-center',
            placement.side === 'top' ? 'bottom-full pb-1.5' : 'top-full pt-1.5',
          )}
        >
          <div
            role="group"
            aria-label={`${name} details`}
            data-testid="map-tooltip"
            ref={setTipEl}
            className="w-64 max-w-[calc(100vw-1rem)] shrink-0 overflow-auto rounded-xl border bg-popover p-3 text-popover-foreground shadow-lg"
            style={{
              transform: `translateX(${placement.shiftX}px)`,
              maxHeight: `${placement.maxHeight}px`,
            }}
          >
            <MapTooltip
              device={device}
              now={now}
              geocodeEnabled={geocodeEnabled}
              onConfirmOpenChange={(held) => {
                hover.setHold(held)
                // Releasing a hold while the pointer is still on the tooltip must not close it.
                if (!held && inside.current) hover.enter(key)
              }}
            />
          </div>
        </div>
      ) : null}
    </div>,
    el,
  )
}
