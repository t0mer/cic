import { AlertTriangle, Maximize2 } from 'lucide-react'
import {
  AttributionControl,
  Map as GLMap,
  type ErrorEvent,
  setWorkerUrl,
  type LngLatBoundsLike,
} from 'maplibre-gl'
import workerUrl from 'maplibre-gl/dist/maplibre-gl-worker.mjs?url'
import 'maplibre-gl/dist/maplibre-gl.css'
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'

import { Button } from '@/components/ui/button'
import { boundsOf } from '@/lib/bounds'
import { spreadOffsets, stableOffsets, type Offset } from '@/lib/cluster'
import { escapeHtml } from '@/lib/ui-config'
import { prefersReducedMotion, useCanHover, useIsNarrow } from '@/lib/media'
import type { Device } from '@/lib/types'
import { MapMarker } from './MapMarker'
import { useHoverIntent } from './useHoverIntent'

// The bundler moves maplibre next to the app code, so the default worker lookup (a sibling file of
// the library module) fails. Serve the worker as a same-origin asset (CSP: worker-src 'self').
setWorkerUrl(workerUrl)

export interface MapSelection {
  key: string
  nonce: number
}

const ZERO: [number, number] = [0, 0]

// Maps already torn down. On unmount the map effect's cleanup runs before the attribution
// effect's, and maplibre throws when a control is removed from a removed map.
const removedMaps = new WeakSet<GLMap>()

export default function MapView({
  devices,
  styleUrl,
  attribution,
  geocodeEnabled,
  now,
  selection,
}: {
  /** Only devices that have a location. */
  devices: Device[]
  styleUrl: string
  attribution: string
  geocodeEnabled: boolean
  now: number
  selection: MapSelection | null
}) {
  const containerRef = useRef<HTMLDivElement>(null)
  const [map, setMap] = useState<GLMap | null>(null)
  const [initError, setInitError] = useState<string | null>(null)
  const [styleError, setStyleError] = useState(false)
  const [attempt, setAttempt] = useState(0)
  const [tick, setTick] = useState(0)
  const canHover = useCanHover()
  const narrow = useIsNarrow()
  const hover = useHoverIntent()

  const styleRef = useRef(styleUrl)
  const appliedStyle = useRef(styleUrl)
  const styleReady = useRef(false)
  const fitted = useRef(false)
  const { hide, suppressFocus } = hover

  useEffect(() => {
    styleRef.current = styleUrl
  }, [styleUrl])

  // Create the map once (and again only on an explicit retry); tear everything down on unmount.
  useEffect(() => {
    const container = containerRef.current
    if (!container) return
    let m: GLMap
    try {
      m = new GLMap({
        container,
        style: styleRef.current,
        center: [0, 20],
        zoom: 1,
        attributionControl: false,
        dragRotate: false,
        pitchWithRotate: false,
      })
    } catch (e) {
      // Reporting a failed external init (e.g. no WebGL) is exactly what an effect is for.
      // eslint-disable-next-line react-hooks/set-state-in-effect
      setInitError(e instanceof Error ? e.message : 'The map could not start')
      return
    }
    appliedStyle.current = styleRef.current
    styleReady.current = false
    // A new map starts at the world view again, so it has to be fitted again.
    fitted.current = false
    m.touchZoomRotate.disableRotation()
    const bump = () => setTick((t) => t + 1)
    const onStyleLoad = () => {
      styleReady.current = true
      setStyleError(false)
    }
    const onError = (e: ErrorEvent) => {
      const ev = e as ErrorEvent & { sourceId?: string; tile?: unknown }
      if (!styleReady.current && !ev.sourceId && !ev.tile) setStyleError(true)
    }
    const onClick = (e: { originalEvent: MouseEvent }) => {
      const t = e.originalEvent.target
      if (!(t instanceof Element) || !t.closest('[data-marker]')) hide()
    }
    m.on('style.load', onStyleLoad)
    m.on('error', onError)
    m.on('click', onClick)
    m.on('load', bump)
    m.on('zoomend', bump)
    m.on('moveend', bump)
    m.on('resize', bump)
    setInitError(null)
    setMap(m)
    return () => {
      removedMaps.add(m)
      m.remove()
      setMap(null)
    }
  }, [attempt, hide])

  // Follow the app theme; DOM markers live outside the style, so they survive setStyle.
  useEffect(() => {
    if (!map || appliedStyle.current === styleUrl) return
    appliedStyle.current = styleUrl
    styleReady.current = false
    map.setStyle(styleUrl)
  }, [map, styleUrl])

  useEffect(() => {
    if (!map) return
    const ctl = new AttributionControl({
      compact: narrow,
      customAttribution: attribution ? escapeHtml(attribution) : undefined,
    })
    map.addControl(ctl, 'bottom-right')
    return () => {
      if (!removedMaps.has(map)) map.removeControl(ctl)
    }
  }, [map, attribution, narrow])

  const fit = useCallback(
    (instant: boolean) => {
      if (!map) return
      const box = boundsOf(
        devices.map((d) => ({ lng: d.longitude as number, lat: d.latitude as number })),
      )
      if (!box) return
      const duration = instant || prefersReducedMotion() ? 0 : 800
      if (box[0][0] === box[1][0] && box[0][1] === box[1][1])
        map.easeTo({ center: box[0], zoom: 14, duration })
      else
        map.fitBounds(box as LngLatBoundsLike, {
          padding: { top: 72, bottom: 72, left: 56, right: 56 },
          maxZoom: 15,
          duration,
        })
    },
    [map, devices],
  )

  // Fit to everything on first load only; afterwards the "Fit all" button does it.
  useEffect(() => {
    if (!map || fitted.current || devices.length === 0) return
    fitted.current = true
    fit(true)
  }, [map, devices, fit])

  useEffect(() => {
    if (!map || !selection) return
    const d = devices.find((x) => x.key === selection.key)
    if (!d) return
    map.flyTo({
      center: [d.longitude as number, d.latitude as number],
      zoom: Math.max(map.getZoom(), 14),
      duration: prefersReducedMotion() ? 0 : 1200,
    })
    hover.show(selection.key)
    // Only a new selection triggers a fly; live device updates must not move the camera.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [selection])

  const activeKey = hover.activeKey
  useEffect(() => {
    if (activeKey === null) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== 'Escape') return
      // Closing the tooltip removes the element that has focus; hand focus back to the marker
      // button (without reopening the tooltip) instead of dropping it on <body>.
      const el = document.activeElement
      if (
        el instanceof HTMLElement &&
        el.closest('[data-marker]') &&
        !el.hasAttribute('data-marker-btn')
      ) {
        const btn = el.closest('[data-marker]')?.querySelector<HTMLElement>('[data-marker-btn]')
        if (btn) {
          suppressFocus()
          btn.focus()
        }
      }
      hide()
    }
    document.addEventListener('keydown', onKey)
    return () => document.removeEventListener('keydown', onKey)
  }, [activeKey, hide, suppressFocus])

  // Offsets only depend on pixel positions; an unrelated live update (another device's battery)
  // must hand the markers the very same objects so nothing is repositioned.
  const [offsets, setOffsets] = useState<Map<string, Offset>>(() => new Map())
  const computed = useMemo(() => {
    if (!map) return null
    void tick
    return spreadOffsets(
      devices.map((d) => {
        const p = map.project([d.longitude as number, d.latitude as number])
        return { id: d.key, x: p.x, y: p.y }
      }),
    )
  }, [map, devices, tick])
  const [prevComputed, setPrevComputed] = useState(computed)
  if (computed !== prevComputed) {
    setPrevComputed(computed)
    if (computed) setOffsets((prev) => stableOffsets(prev, computed))
  }

  const retryStyle = () => {
    if (!map) return
    setStyleError(false)
    styleReady.current = false
    map.setStyle(styleUrl)
  }

  return (
    <div className="relative size-full min-h-0 overflow-hidden rounded-xl border bg-muted">
      {/* maplibre's own (unlayered) CSS sets position:relative on its container, which would beat a
          Tailwind `absolute`, so the container only fills a positioned wrapper. */}
      <div className="absolute inset-0">
        <div ref={containerRef} className="size-full" data-testid="map-canvas" />
      </div>
      {map ? (
        <>
          <Button
            variant="outline"
            size="icon"
            className="absolute start-3 top-3 z-10 size-11"
            aria-label="Fit all"
            title="Fit all"
            onClick={() => fit(false)}
          >
            <Maximize2 aria-hidden="true" />
          </Button>
          {devices.map((d) => (
            <MapMarker
              key={d.key}
              map={map}
              device={d}
              offset={offsets.get(d.key) ?? ZERO}
              tick={tick}
              active={activeKey === d.key}
              canHover={canHover}
              now={now}
              geocodeEnabled={geocodeEnabled}
              hover={hover}
            />
          ))}
        </>
      ) : null}
      {initError || styleError ? (
        <div
          role="alert"
          className="absolute inset-x-3 top-16 z-10 mx-auto flex max-w-sm flex-col items-center gap-2 rounded-xl border bg-background p-4 text-center text-sm shadow-lg"
        >
          <AlertTriangle aria-hidden="true" className="size-6 text-muted-foreground" />
          <p>
            {initError
              ? 'The map could not start on this device. The device list still works.'
              : 'The map style failed to load.'}
          </p>
          <Button
            variant="outline"
            className="h-11 px-6"
            onClick={() => (initError ? setAttempt((a) => a + 1) : retryStyle())}
          >
            Retry
          </Button>
        </div>
      ) : null}
    </div>
  )
}
