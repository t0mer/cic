import { AlertTriangle, Maximize2, X } from 'lucide-react'
import {
  AttributionControl,
  Map as GLMap,
  type ErrorEvent,
  type GeoJSONSource,
  type LngLatBoundsLike,
  type MapLayerMouseEvent,
  setWorkerUrl,
} from 'maplibre-gl'
import workerUrl from 'maplibre-gl/dist/maplibre-gl-worker.mjs?url'
import 'maplibre-gl/dist/maplibre-gl.css'
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'

import { Button } from '@/components/ui/button'
import { buildFeatures, formatDateTime, type DayRow } from '@/lib/history-days'
import { prefersReducedMotion, useIsNarrow } from '@/lib/media'
import { escapeHtml } from '@/lib/ui-config'

// Same-origin worker asset (CSP: worker-src 'self'), exactly as the live map does.
setWorkerUrl(workerUrl)

// Maps already torn down: the attribution cleanup must not touch a removed map.
const removedMaps = new WeakSet<GLMap>()

const SRC = { lines: 'history-lines', points: 'history-points', ends: 'history-ends' } as const
const LAYER = {
  casing: 'history-casing',
  line: 'history-line',
  points: 'history-points',
  ends: 'history-ends',
} as const

interface PickedPoint {
  t: number
  lat: number
  lon: number
}

export default function HistoryMap({
  days,
  styleUrl,
  attribution,
  accuracyByT,
  fitKey,
}: {
  /** Only the days that are switched on. */
  days: DayRow[]
  styleUrl: string
  attribution: string
  accuracyByT: ReadonlyMap<number, number | null>
  /** A new value re-fits the camera (device or range changed), data edits alone do not. */
  fitKey: string
}) {
  const containerRef = useRef<HTMLDivElement>(null)
  const [map, setMap] = useState<GLMap | null>(null)
  const [initError, setInitError] = useState<string | null>(null)
  const [styleError, setStyleError] = useState(false)
  const [attempt, setAttempt] = useState(0)
  const [ready, setReady] = useState(0)
  const [picked, setPicked] = useState<PickedPoint | null>(null)
  const narrow = useIsNarrow()

  const features = useMemo(() => buildFeatures(days), [days])
  const featuresRef = useRef(features)
  const styleRef = useRef(styleUrl)
  const appliedStyle = useRef(styleUrl)
  const styleReady = useRef(false)
  const fittedFor = useRef<string | null>(null)

  useEffect(() => {
    styleRef.current = styleUrl
  }, [styleUrl])

  // Adds the sources/layers when missing (a style change wipes them) and refreshes their data.
  const sync = useCallback((m: GLMap) => {
    if (removedMaps.has(m) || !styleReady.current) return
    const f = featuresRef.current
    const data = { lines: f.lines, points: f.points, ends: f.ends }
    for (const k of ['lines', 'points', 'ends'] as const) {
      const src = m.getSource(SRC[k]) as GeoJSONSource | undefined
      if (src) src.setData(data[k])
      else m.addSource(SRC[k], { type: 'geojson', data: data[k] })
    }
    if (!m.getLayer(LAYER.casing))
      m.addLayer({
        id: LAYER.casing,
        type: 'line',
        source: SRC.lines,
        layout: { 'line-cap': 'round', 'line-join': 'round' },
        paint: { 'line-color': '#ffffff', 'line-width': 7, 'line-opacity': 0.85 },
      })
    if (!m.getLayer(LAYER.line))
      m.addLayer({
        id: LAYER.line,
        type: 'line',
        source: SRC.lines,
        layout: { 'line-cap': 'round', 'line-join': 'round' },
        paint: { 'line-color': ['get', 'color'], 'line-width': 4 },
      })
    if (!m.getLayer(LAYER.points))
      m.addLayer({
        id: LAYER.points,
        type: 'circle',
        source: SRC.points,
        minzoom: 11,
        paint: {
          'circle-radius': 4,
          'circle-color': ['get', 'color'],
          'circle-stroke-color': '#ffffff',
          'circle-stroke-width': 1.5,
        },
      })
    if (!m.getLayer(LAYER.ends))
      m.addLayer({
        id: LAYER.ends,
        type: 'circle',
        source: SRC.ends,
        paint: {
          'circle-radius': 8,
          'circle-color': ['match', ['get', 'kind'], 'start', '#15803d', '#b91c1c'],
          'circle-stroke-color': '#ffffff',
          'circle-stroke-width': 2.5,
        },
      })
  }, [])

  const fit = useCallback(
    (instant: boolean) => {
      const box = featuresRef.current.bounds
      if (!map || !box) return
      const duration = instant || prefersReducedMotion() ? 0 : 800
      if (box[0][0] === box[1][0] && box[0][1] === box[1][1])
        map.easeTo({ center: box[0], zoom: 14, duration })
      else
        map.fitBounds(box as LngLatBoundsLike, {
          padding: { top: 56, bottom: 56, left: 48, right: 48 },
          maxZoom: 16,
          duration,
        })
    },
    [map],
  )

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
      // Reporting a failed external init (e.g. no WebGL) is what an effect is for.
      // eslint-disable-next-line react-hooks/set-state-in-effect
      setInitError(e instanceof Error ? e.message : 'The map could not start')
      return
    }
    appliedStyle.current = styleRef.current
    styleReady.current = false
    fittedFor.current = null
    m.touchZoomRotate.disableRotation()
    const onStyleLoad = () => {
      styleReady.current = true
      setStyleError(false)
      sync(m)
      setReady((r) => r + 1) // lets the data effect fit the camera once the style is usable
    }
    const onError = (e: ErrorEvent) => {
      const ev = e as ErrorEvent & { sourceId?: string; tile?: unknown }
      if (!styleReady.current && !ev.sourceId && !ev.tile) setStyleError(true)
    }
    const pick = (e: MapLayerMouseEvent) => {
      const f = e.features?.[0]
      if (!f || f.geometry.type !== 'Point') return
      const [lon, lat] = f.geometry.coordinates
      setPicked({ t: Number(f.properties?.t), lat, lon })
    }
    const pointer = () => (m.getCanvas().style.cursor = 'pointer')
    const reset = () => (m.getCanvas().style.cursor = '')
    m.on('style.load', onStyleLoad)
    m.on('error', onError)
    m.on('click', LAYER.points, pick)
    m.on('click', LAYER.ends, pick)
    m.on('mouseenter', LAYER.points, pointer)
    m.on('mouseleave', LAYER.points, reset)
    setInitError(null)
    setMap(m)
    return () => {
      removedMaps.add(m)
      m.remove()
      setMap(null)
    }
  }, [attempt, sync])

  // Follow the app theme; setStyle drops our layers, style.load puts them back.
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

  // New data: redraw, and fit when the device/range changed.
  useEffect(() => {
    featuresRef.current = features
    if (!map) return
    sync(map)
    if (styleReady.current && fittedFor.current !== fitKey && features.bounds) {
      fittedFor.current = fitKey
      fit(true)
    }
  }, [map, features, fitKey, sync, fit, ready])

  const [prevKey, setPrevKey] = useState(fitKey)
  if (prevKey !== fitKey) {
    setPrevKey(fitKey)
    setPicked(null)
  }

  const retryStyle = () => {
    if (!map) return
    setStyleError(false)
    styleReady.current = false
    map.setStyle(styleUrl)
  }

  const acc = picked ? accuracyByT.get(picked.t) : undefined

  return (
    <div className="relative size-full min-h-0 overflow-hidden rounded-xl border bg-muted">
      <div className="absolute inset-0">
        <div ref={containerRef} className="size-full" data-testid="history-map-canvas" />
      </div>
      {map ? (
        <Button
          variant="outline"
          size="icon"
          className="absolute start-3 top-3 z-10 size-11"
          aria-label="Fit route"
          title="Fit route"
          onClick={() => fit(false)}
        >
          <Maximize2 aria-hidden="true" />
        </Button>
      ) : null}
      {picked ? (
        <div
          role="status"
          data-testid="history-point-info"
          className="absolute start-3 bottom-8 z-10 flex max-w-[calc(100%-1.5rem)] items-start gap-2 rounded-lg border bg-background p-3 text-sm shadow-lg"
        >
          <div className="min-w-0">
            <p className="font-medium">{formatDateTime(picked.t)}</p>
            <p className="text-xs text-muted-foreground">
              {picked.lat.toFixed(5)}, {picked.lon.toFixed(5)}
            </p>
            <p className="text-xs text-muted-foreground">
              Accuracy: {typeof acc === 'number' ? `±${Math.round(acc)} m` : 'unknown'}
            </p>
          </div>
          <Button
            variant="ghost"
            className="-m-2 size-11"
            aria-label="Close point details"
            onClick={() => setPicked(null)}
          >
            <X aria-hidden="true" />
          </Button>
        </div>
      ) : null}
      {initError || styleError ? (
        <div
          role="alert"
          className="absolute inset-x-3 top-16 z-10 mx-auto flex max-w-sm flex-col items-center gap-2 rounded-xl border bg-background p-4 text-center text-sm shadow-lg"
        >
          <AlertTriangle aria-hidden="true" className="size-6 text-muted-foreground" />
          <p>
            {initError
              ? 'The map could not start on this device. The day list and points still work.'
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
