import { AlertTriangle, ArrowLeft, LockKeyhole, MapPinOff } from 'lucide-react'
import { lazy, Suspense, useMemo, useState } from 'react'
import { Link, useParams, useSearchParams } from 'react-router-dom'

import { Button } from '@/components/ui/button'
import { useAuth } from '@/features/auth/auth-context'
import { useUiConfig } from '@/features/config/queries'
import { useDevices } from '@/features/devices/queries'
import { ApiError } from '@/lib/api'
import { formatDayLabel, groupDays } from '@/lib/history-days'
import {
  browserTimeZone,
  presetRange,
  resolveRange,
  withRange,
  type Preset,
} from '@/lib/history-range'
import { useNow } from '@/lib/now'
import { useIsDark } from '@/lib/use-dark'
import { cn } from '@/lib/utils'
import { DayList } from './DayList'
import { PointsTable } from './PointsTable'
import { useHistory, useRoutes } from './queries'
import { RangeFilter } from './RangeFilter'

// maplibre-gl is large: keep it out of the main bundle, like the live map.
const HistoryMap = lazy(() => import('./HistoryMap'))

type Tab = 'route' | 'points'

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
    <div className="flex flex-col items-center gap-3 rounded-xl border border-dashed p-8 text-center">
      <div aria-hidden="true" className="text-muted-foreground [&>svg]:size-10">
        {icon}
      </div>
      <h2 className="text-lg font-semibold">{title}</h2>
      {children}
    </div>
  )
}

function Loading() {
  return (
    <div role="status" aria-busy="true" className="grid gap-3 lg:grid-cols-[minmax(0,1fr)_20rem]">
      <span className="sr-only">Loading history</span>
      <div className="h-[55dvh] min-h-72 animate-pulse rounded-xl border bg-accent" />
      <div className="flex flex-col gap-2">
        {[0, 1, 2].map((i) => (
          <div key={i} className="h-16 animate-pulse rounded-lg bg-accent" />
        ))}
      </div>
    </div>
  )
}

export function HistoryPage() {
  const { prefix = '', device = '' } = useParams()
  const { isAdmin } = useAuth()
  const [params, setParams] = useSearchParams()
  const now = useNow()
  const dark = useIsDark()
  const cfgQ = useUiConfig()
  const devicesQ = useDevices()
  const [tab, setTab] = useState<Tab>('route')
  const [hiddenDays, setHiddenDays] = useState<ReadonlySet<string>>(new Set())

  const range = useMemo(() => resolveRange(params, new Date(now * 1000)), [params, now])
  const apiRange = useMemo(
    () =>
      range.from && range.to
        ? { from: range.from.toISOString(), to: range.to.toISOString() }
        : null,
    [range.from, range.to],
  )
  const tz = useMemo(() => browserTimeZone(), [])
  const recordingOff = cfgQ.data?.history_enabled === false
  const enabled = isAdmin
  const routesQ = useRoutes(prefix, device, apiRange, tz, enabled)
  // Raw points feed the table and the accuracy of a tapped map point.
  const historyQ = useHistory(prefix, device, apiRange, enabled && !!routesQ.data)

  const days = useMemo(() => groupDays(routesQ.data?.days ?? []), [routesQ.data])
  const visible = useMemo(() => days.filter((d) => !hiddenDays.has(d.date)), [days, hiddenDays])
  const accuracyByT = useMemo(
    () => new Map((historyQ.data?.points ?? []).map((p) => [p.t, p.accuracy] as const)),
    [historyQ.data],
  )

  const deviceKey = `${prefix}/${device}`
  const name = devicesQ.data?.find((d) => d.key === deviceKey)?.full_name || device
  const fitKey = `${deviceKey}|${apiRange?.from}|${apiRange?.to}|${tz}`

  const setRange = (r: { from?: string; to?: string }) => {
    setParams((prev) => withRange(prev, r), { replace: true })
    setHiddenDays(new Set())
  }
  const onPreset = (p: Preset) => setRange(presetRange(p, new Date(now * 1000)))
  const toggleDay = (date: string) =>
    setHiddenDays((prev) => {
      const next = new Set(prev)
      if (next.has(date)) next.delete(date)
      else next.add(date)
      return next
    })

  const header = (
    <div className="flex flex-col gap-2">
      <Link
        to="/devices"
        className="inline-flex h-11 w-fit items-center gap-1.5 rounded-md pe-3 text-sm font-medium underline-offset-4 hover:underline focus-visible:ring-[3px] focus-visible:ring-ring focus-visible:outline-none"
      >
        <ArrowLeft aria-hidden="true" className="size-4" />
        Back to devices
      </Link>
      <h1 dir="auto" className="truncate text-xl font-semibold">
        Location history · {name}
      </h1>
    </div>
  )

  const wrap = (body: React.ReactNode) => (
    <div className="flex min-w-0 flex-col gap-4">
      {header}
      {body}
    </div>
  )

  if (!isAdmin)
    return wrap(
      <Notice icon={<LockKeyhole />} title="Location history is for administrators only" />,
    )

  const err = routesQ.error ?? historyQ.error
  let body: React.ReactNode
  if (!apiRange) body = null
  else if (routesQ.isPending) body = <Loading />
  else if (routesQ.error && !routesQ.data) {
    const e = routesQ.error
    body =
      e instanceof ApiError && e.status === 403 ? (
        <Notice icon={<LockKeyhole />} title="You do not have access to location history">
          <p role="alert" className="max-w-prose text-sm text-muted-foreground">
            Only administrators can view where a phone has been.
          </p>
        </Notice>
      ) : (
        <Notice icon={<AlertTriangle />} title="Could not load history">
          <p role="alert" className="max-w-prose text-sm text-muted-foreground">
            {e instanceof Error ? e.message : 'Unexpected error'}
          </p>
          <Button
            variant="outline"
            className="h-11 px-6"
            disabled={routesQ.isFetching}
            onClick={() => void routesQ.refetch()}
          >
            Retry
          </Button>
        </Notice>
      )
  } else if (days.length === 0)
    body = (
      <Notice icon={<MapPinOff />} title="No locations recorded in this range">
        <p className="max-w-prose text-sm text-muted-foreground">
          Try a wider range, or check that the phone has reported its location.
        </p>
      </Notice>
    )
  else {
    const cfg = cfgQ.data
    const truncated = historyQ.data?.truncated === true
    body = (
      <div className="flex min-w-0 flex-col gap-3">
        {recordingOff ? (
          <p
            role="status"
            className="rounded-md border border-amber-300 bg-amber-50 px-3 py-2 text-sm text-amber-900 dark:border-amber-800 dark:bg-amber-950 dark:text-amber-200"
          >
            New locations are not being recorded; showing stored history.
          </p>
        ) : null}
        {err ? (
          <p
            role="status"
            className="rounded-md border border-amber-300 bg-amber-50 px-3 py-2 text-sm text-amber-900 dark:border-amber-800 dark:bg-amber-950 dark:text-amber-200"
          >
            Some data could not be refreshed. Showing what was loaded.
          </p>
        ) : null}
        <div role="tablist" aria-label="History views" className="flex gap-2">
          {(['route', 'points'] as const).map((t) => (
            <button
              key={t}
              type="button"
              role="tab"
              id={`history-tab-${t}`}
              aria-selected={tab === t}
              aria-controls={`history-panel-${t}`}
              onClick={() => setTab(t)}
              className={cn(
                'h-11 rounded-md border px-4 text-sm font-medium focus-visible:ring-[3px] focus-visible:ring-ring focus-visible:outline-none',
                tab === t ? 'bg-primary text-primary-foreground' : 'bg-background hover:bg-accent',
              )}
            >
              {t === 'route' ? 'Route' : 'Points'}
            </button>
          ))}
        </div>
        <div
          role="tabpanel"
          id="history-panel-route"
          aria-labelledby="history-tab-route"
          hidden={tab !== 'route'}
          className="grid min-w-0 gap-3 lg:grid-cols-[minmax(0,1fr)_20rem]"
        >
          <div className="flex min-w-0 flex-col gap-2">
            <div className="h-[55dvh] min-h-72 lg:h-[calc(100dvh-20rem)]">
              {cfgQ.error && !cfg ? (
                <div className="flex h-full flex-col items-center justify-center gap-2 rounded-xl border border-dashed p-4 text-center">
                  <p role="alert" className="text-sm text-destructive">
                    Could not load the map configuration.
                  </p>
                  <Button
                    variant="outline"
                    className="h-11 px-6"
                    disabled={cfgQ.isFetching}
                    onClick={() => void cfgQ.refetch()}
                  >
                    Retry
                  </Button>
                </div>
              ) : cfg ? (
                <Suspense
                  fallback={
                    <div role="status" className="p-4 text-sm text-muted-foreground">
                      Loading map…
                    </div>
                  }
                >
                  <HistoryMap
                    days={visible}
                    styleUrl={dark ? cfg.map.style_dark : cfg.map.style_light}
                    attribution={cfg.map.attribution}
                    accuracyByT={accuracyByT}
                    fitKey={fitKey}
                  />
                </Suspense>
              ) : (
                <div
                  role="status"
                  aria-busy="true"
                  className="h-full animate-pulse rounded-xl border bg-accent"
                >
                  <span className="sr-only">Loading map</span>
                </div>
              )}
            </div>
            <ul
              aria-label="Legend"
              data-testid="history-legend"
              className="flex flex-wrap items-center gap-x-4 gap-y-1 text-xs text-muted-foreground"
            >
              <li className="flex items-center gap-1.5">
                <span aria-hidden="true" className="size-3 rounded-full bg-[#15803d]" />
                Start
              </li>
              <li className="flex items-center gap-1.5">
                <span aria-hidden="true" className="size-3 rounded-full bg-[#b91c1c]" />
                End
              </li>
              {visible.map((d) => (
                <li key={d.date} className="flex items-center gap-1.5">
                  <span
                    aria-hidden="true"
                    className="h-1 w-4 rounded"
                    style={{ backgroundColor: d.color }}
                  />
                  {formatDayLabel(d.date)}
                </li>
              ))}
            </ul>
          </div>
          <DayList days={days} hidden={hiddenDays} onToggle={toggleDay} />
        </div>
        <div
          role="tabpanel"
          id="history-panel-points"
          aria-labelledby="history-tab-points"
          hidden={tab !== 'points'}
          className="flex min-w-0 flex-col gap-3"
        >
          {truncated ? (
            <p
              role="status"
              className="rounded-md border border-amber-300 bg-amber-50 px-3 py-2 text-sm text-amber-900 dark:border-amber-800 dark:bg-amber-950 dark:text-amber-200"
            >
              Showing a sample of the points in this range. Narrow the range to see every point.
            </p>
          ) : null}
          {historyQ.isPending ? (
            <div role="status" aria-busy="true" className="h-40 animate-pulse rounded-lg bg-accent">
              <span className="sr-only">Loading points</span>
            </div>
          ) : historyQ.error && !historyQ.data ? (
            <div className="flex flex-col items-start gap-2 rounded-lg border border-dashed p-4">
              <p role="alert" className="text-sm text-destructive">
                Could not load the points.
              </p>
              <Button
                variant="outline"
                className="h-11 px-4"
                onClick={() => void historyQ.refetch()}
              >
                Retry
              </Button>
            </div>
          ) : (
            <PointsTable key={fitKey} points={historyQ.data?.points ?? []} />
          )}
        </div>
      </div>
    )
  }

  return wrap(
    <>
      <RangeFilter
        range={range}
        onFrom={(v) => setRange({ from: v })}
        onTo={(v) => setRange({ to: v })}
        onPreset={onPreset}
      />
      {body}
    </>,
  )
}
