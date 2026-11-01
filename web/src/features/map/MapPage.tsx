import { AlertTriangle, ChevronUp, MapPinOff } from 'lucide-react'
import { useMemo, useState } from 'react'

import { Button } from '@/components/ui/button'
import { useUiConfig } from '@/features/config/queries'
import { useDevices } from '@/features/devices/queries'
import { hasLocation } from '@/lib/location'
import { useIsNarrow } from '@/lib/media'
import { useNow } from '@/lib/now'
import { useIsDark } from '@/lib/use-dark'
import { cn } from '@/lib/utils'
import { MapDeviceList } from './MapDeviceList'
import MapView, { type MapSelection } from './MapView'

function Notice({
  icon,
  title,
  children,
}: {
  icon: React.ReactNode
  title: string
  children?: React.ReactNode
}) {
  return (
    <div className="flex size-full flex-col items-center justify-center gap-3 rounded-xl border border-dashed p-8 text-center">
      <div aria-hidden="true" className="text-muted-foreground [&>svg]:size-10">
        {icon}
      </div>
      <h2 className="text-lg font-semibold">{title}</h2>
      {children}
    </div>
  )
}

const frame = 'h-[calc(100dvh-10.5rem)] min-h-80 md:h-[calc(100dvh-7rem)]'

export function MapPage() {
  const now = useNow()
  const dark = useIsDark()
  const narrow = useIsNarrow()
  const devicesQ = useDevices()
  const cfgQ = useUiConfig()
  const [query, setQuery] = useState('')
  const [open, setOpen] = useState(false)
  const [selection, setSelection] = useState<MapSelection | null>(null)

  const data = devicesQ.data
  const all = useMemo(() => (data ?? []).filter((d) => !d.hidden), [data])
  const located = useMemo(() => all.filter(hasLocation), [all])

  const heading = <h1 className="sr-only">Map</h1>

  if ((devicesQ.isPending && !devicesQ.error) || (cfgQ.isPending && !cfgQ.error)) {
    return (
      <>
        {heading}
        <div
          role="status"
          aria-busy="true"
          className={cn(frame, 'animate-pulse rounded-xl border bg-accent')}
        >
          <span className="sr-only">Loading map</span>
        </div>
      </>
    )
  }

  const failure =
    devicesQ.error && !devicesQ.data ? devicesQ : cfgQ.error && !cfgQ.data ? cfgQ : null
  if (failure || !devicesQ.data || !cfgQ.data) {
    const err = failure?.error
    return (
      <>
        {heading}
        <div className={frame}>
          <Notice icon={<AlertTriangle />} title="Could not load the map">
            <p role="alert" className="max-w-prose text-sm text-muted-foreground">
              {err instanceof Error ? err.message : 'Unexpected response'}
            </p>
            <Button
              variant="outline"
              className="h-11 px-6"
              onClick={() => {
                if (failure) void failure.refetch()
                else {
                  void devicesQ.refetch()
                  void cfgQ.refetch()
                }
              }}
            >
              Retry
            </Button>
          </Notice>
        </div>
      </>
    )
  }

  const cfg = cfgQ.data
  const stale = devicesQ.error ? (
    <p
      role="status"
      className="mb-2 rounded-md border border-amber-300 bg-amber-50 px-3 py-2 text-sm text-amber-900 dark:border-amber-800 dark:bg-amber-950 dark:text-amber-200"
    >
      Data may be out of date — retrying
    </p>
  ) : null

  const select = (key: string) => {
    setSelection((s) => ({ key, nonce: (s?.nonce ?? 0) + 1 }))
    if (narrow) setOpen(false)
  }

  const list = <MapDeviceList devices={all} query={query} onQuery={setQuery} onSelect={select} />

  const mapArea =
    located.length === 0 ? (
      <Notice icon={<MapPinOff />} title="No phones with a location yet" />
    ) : (
      <MapView
        devices={located}
        styleUrl={dark ? cfg.map.style_dark : cfg.map.style_light}
        attribution={cfg.map.attribution}
        geocodeEnabled={cfg.geocode_enabled}
        now={now}
        selection={selection}
      />
    )

  return (
    <>
      {heading}
      {stale}
      <div className={cn(frame, 'flex gap-3', narrow ? 'flex-col' : 'flex-row')}>
        {narrow ? null : (
          <aside
            aria-label="Phones"
            className="flex w-80 shrink-0 flex-col gap-2 rounded-xl border bg-card p-2"
          >
            {list}
          </aside>
        )}
        <div className="min-h-0 min-w-0 flex-1">{mapArea}</div>
        {narrow ? (
          <section
            aria-label="Phones"
            data-testid="bottom-sheet"
            className={cn(
              'flex shrink-0 flex-col rounded-xl border bg-card',
              open && 'max-h-[55%] min-h-0',
            )}
          >
            <button
              type="button"
              aria-expanded={open}
              aria-controls="map-sheet-list"
              onClick={() => setOpen((o) => !o)}
              className="flex min-h-12 w-full items-center justify-between gap-2 rounded-xl px-4 text-sm font-medium focus-visible:ring-[3px] focus-visible:ring-ring focus-visible:outline-none"
            >
              <span>
                {all.length} {all.length === 1 ? 'phone' : 'phones'}
              </span>
              <ChevronUp
                aria-hidden="true"
                className={cn('size-5 transition-transform', open && 'rotate-180')}
              />
            </button>
            <div
              id="map-sheet-list"
              hidden={!open}
              className="flex min-h-0 flex-1 flex-col p-2 pt-0"
            >
              {open ? list : null}
            </div>
          </section>
        ) : null}
      </div>
    </>
  )
}
