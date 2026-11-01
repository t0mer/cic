import { useEffect } from 'react'
import { AlertTriangle, SearchX, SmartphoneNfc } from 'lucide-react'
import { useSearchParams } from 'react-router-dom'
import { toast } from 'sonner'

import { Button } from '@/components/ui/button'
import { useUiConfig } from '@/features/config/queries'
import {
  filterDevices,
  SORT_KEYS,
  sortDevices,
  STATUS_FILTERS,
  type SortKey,
  type StatusFilter,
} from '@/lib/device-list'
import { useNow } from '@/lib/now'
import { DeviceCard, DeviceCardSkeleton } from './DeviceCard'
import { useDevices } from './queries'
import { Toolbar } from './Toolbar'

function Message({
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

export function DevicesPage() {
  const [params, setParams] = useSearchParams()
  const now = useNow()
  const { data, isPending, error, refetch, isFetching } = useDevices()
  const geocodeEnabled = useUiConfig().data?.geocode_enabled ?? false

  const query = params.get('q') ?? ''
  const statusParam = params.get('status')
  const status: StatusFilter = STATUS_FILTERS.find((s) => s === statusParam) ?? 'all'
  const sortParam = params.get('sort')
  const sort: SortKey = SORT_KEYS.find((s) => s === sortParam) ?? 'name'

  const update = (key: string, value: string, def: string) =>
    setParams(
      (prev) => {
        const next = new URLSearchParams(prev)
        if (value && value !== def) next.set(key, value)
        else next.delete(key)
        return next
      },
      { replace: true },
    )

  const errMsg = error instanceof Error ? error.message : null
  useEffect(() => {
    if (errMsg && !(error as { status?: number }).status)
      toast.error(`Could not load devices: ${errMsg}`)
  }, [errMsg, error])

  const heading = <h1 className="sr-only">Devices</h1>

  if (isPending && !error) {
    return (
      <>
        {heading}
        <div
          role="status"
          aria-busy="true"
          className="grid gap-4 sm:grid-cols-2 xl:grid-cols-3 2xl:grid-cols-4"
        >
          <span className="sr-only">Loading devices</span>
          {Array.from({ length: 6 }, (_, i) => (
            <DeviceCardSkeleton key={i} />
          ))}
        </div>
      </>
    )
  }

  if (error && !data) {
    const status503 = (error as { status?: number }).status === 503
    return (
      <>
        {heading}
        <Message
          icon={<AlertTriangle />}
          title={status503 ? 'API unavailable' : 'Could not load devices'}
        >
          <p role="alert" className="max-w-prose text-sm text-muted-foreground">
            {errMsg}
          </p>
          <Button
            variant="outline"
            className="h-11 px-6"
            onClick={() => void refetch()}
            disabled={isFetching}
          >
            Retry
          </Button>
        </Message>
      </>
    )
  }

  const stale = error ? (
    <p
      role="status"
      className="rounded-md border border-amber-300 bg-amber-50 px-3 py-2 text-sm text-amber-900 dark:border-amber-800 dark:bg-amber-950 dark:text-amber-200"
    >
      Data may be out of date — retrying
    </p>
  ) : null

  const devices = data ?? []
  if (devices.length === 0) {
    return (
      <div className="flex flex-col gap-4">
        {heading}
        {stale}
        <Message icon={<SmartphoneNfc />} title="No phones yet — point DRADIS at the same broker" />
      </div>
    )
  }

  const shown = sortDevices(filterDevices(devices, query, status), sort)
  return (
    <div className="flex flex-col gap-4">
      {heading}
      {stale}
      <Toolbar
        query={query}
        status={status}
        sort={sort}
        count={shown.length}
        total={devices.length}
        onQuery={(v) => update('q', v, '')}
        onStatus={(v) => update('status', v, 'all')}
        onSort={(v) => update('sort', v, 'name')}
      />
      {shown.length === 0 ? (
        <Message icon={<SearchX />} title="No devices match your filters" />
      ) : (
        <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-3 2xl:grid-cols-4">
          {shown.map((d) => (
            <DeviceCard key={d.key} device={d} now={now} geocodeEnabled={geocodeEnabled} />
          ))}
        </div>
      )}
    </div>
  )
}
