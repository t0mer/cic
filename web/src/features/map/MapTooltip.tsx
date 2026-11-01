import { BatteryFull, BatteryLow, ExternalLink, MapPin, Zap } from 'lucide-react'

import { useAuth } from '@/features/auth/auth-context'
import { HistoryLink } from '@/features/history/HistoryLink'
import { PingControl } from '@/features/commands/PingControl'
import { PlacePrefix } from '@/features/devices/PlaceBadge'
import { describeLocation } from '@/lib/location'
import { googleMapsUrl } from '@/lib/maps'
import { relativeTime } from '@/lib/time'
import type { Device } from '@/lib/types'
import { cn } from '@/lib/utils'

export function BatteryLine({ device }: { device: Device }) {
  const level = device.battery_level
  if (level === null) return <span className="text-muted-foreground">Battery unknown</span>
  const pct = Math.max(0, Math.min(100, level))
  const low = pct < 20
  const Icon = low ? BatteryLow : BatteryFull
  return (
    <span
      className={cn(
        'inline-flex items-center gap-1.5',
        low && 'text-amber-700 dark:text-amber-400',
      )}
    >
      <Icon aria-hidden="true" className="size-4" />
      <span className="font-medium tabular-nums">{pct}%</span>
      {device.charging ? (
        <>
          <Zap aria-hidden="true" className="size-3.5" />
          <span className="sr-only">Charging</span>
        </>
      ) : null}
      {low ? <span className="sr-only">Low battery</span> : null}
    </span>
  )
}

/** Content of the map popover. The Ping control is rendered for admins only. */
export function MapTooltip({
  device,
  now,
  geocodeEnabled,
  onConfirmOpenChange,
}: {
  device: Device
  now: number
  geocodeEnabled: boolean
  onConfirmOpenChange?: (open: boolean) => void
}) {
  const { isAdmin } = useAuth()
  const name = device.full_name || device.device
  const view = describeLocation(device, geocodeEnabled, now)
  const updated = device.location_time ?? (device.last_seen > 0 ? device.last_seen : null)
  return (
    <div className="flex flex-col gap-2 text-sm">
      <p dir="auto" className="truncate text-base leading-tight font-semibold" title={name}>
        {name}
      </p>
      <div className="flex items-center gap-3">
        <BatteryLine device={device} />
        <span
          className={device.online ? 'text-green-700 dark:text-green-400' : 'text-muted-foreground'}
        >
          {device.online ? 'Online' : 'Offline'}
        </span>
      </div>
      <div className="flex items-start gap-1.5">
        <MapPin aria-hidden="true" className="mt-0.5 size-4 shrink-0 text-muted-foreground" />
        <div className="min-w-0">
          {view.kind === 'named' ? (
            <>
              <span dir="auto" className="block break-words">
                <PlacePrefix name={view.place} />
                {view.name}
              </span>
              <span className="block text-xs text-muted-foreground">{view.coords}</span>
            </>
          ) : view.kind === 'pending' ? (
            <>
              {view.place ? (
                <span className="block">
                  <PlacePrefix name={view.place} />
                </span>
              ) : null}
              <span
                role="status"
                aria-label="Looking up place name"
                className="my-1 block h-3.5 w-32 animate-pulse rounded bg-accent"
              />
              <span className="block text-xs text-muted-foreground">{view.coords}</span>
            </>
          ) : view.kind === 'coords' ? (
            <span dir="auto" className="break-words">
              <PlacePrefix name={view.place} />
              {view.coords}
            </span>
          ) : (
            <span className="text-muted-foreground">No location</span>
          )}
        </div>
      </div>
      <p className="text-xs text-muted-foreground">
        Last updated {updated ? relativeTime(updated, now) : 'never'}
      </p>
      <div className="flex flex-wrap items-center gap-1.5">
        {isAdmin ? <PingControl device={device} onConfirmOpenChange={onConfirmOpenChange} /> : null}
        {isAdmin ? <HistoryLink device={device} /> : null}
        {view.kind !== 'unknown' && device.latitude !== null && device.longitude !== null ? (
          <a
            href={googleMapsUrl(device.latitude, device.longitude)}
            target="_blank"
            rel="noopener noreferrer"
            className="inline-flex h-11 items-center gap-1.5 rounded-md px-3 text-sm font-medium underline-offset-4 hover:bg-accent hover:underline focus-visible:ring-[3px] focus-visible:ring-ring focus-visible:outline-none"
          >
            <ExternalLink aria-hidden="true" className="size-4" />
            Open in Google Maps
          </a>
        ) : null}
      </div>
    </div>
  )
}
