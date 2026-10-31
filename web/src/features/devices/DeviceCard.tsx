import {
  BatteryCharging,
  BatteryFull,
  ExternalLink,
  MapPin,
  Smartphone,
  Wifi,
  WifiOff,
} from 'lucide-react'

import { Card } from '@/components/ui/card'
import { useAuth } from '@/features/auth/auth-context'
import { DeviceActions } from '@/features/commands/DeviceActions'
import { colorForKey, readableText } from '@/lib/color'
import { firstGrapheme } from '@/lib/grapheme'
import { formatCoords, googleMapsUrl } from '@/lib/maps'
import { relativeTime } from '@/lib/time'
import type { Device } from '@/lib/types'
import { cn } from '@/lib/utils'

function Avatar({ device }: { device: Device }) {
  const bg = colorForKey(device.key, device.color)
  return (
    <div
      aria-hidden="true"
      className="flex size-12 shrink-0 items-center justify-center rounded-full text-xl font-semibold"
      style={{ backgroundColor: bg, color: readableText(bg) }}
    >
      {firstGrapheme(device.full_name || device.device)}
    </div>
  )
}

function OnlineBadge({ online }: { online: boolean }) {
  return (
    <span
      className={cn(
        'inline-flex shrink-0 items-center gap-1.5 rounded-full px-2.5 py-1 text-xs font-medium',
        online
          ? 'bg-green-100 text-green-900 dark:bg-green-950 dark:text-green-300'
          : 'bg-muted text-muted-foreground',
      )}
    >
      <span
        aria-hidden="true"
        className={cn(
          'size-2 rounded-full',
          online ? 'bg-green-600 dark:bg-green-400' : 'bg-zinc-500',
        )}
      />
      {online ? 'Online' : 'Offline'}
    </span>
  )
}

function Row({ icon, children }: { icon: React.ReactNode; children: React.ReactNode }) {
  return (
    <div className="flex min-h-6 items-center gap-2 text-sm">
      <span aria-hidden="true" className="text-muted-foreground [&>svg]:size-4">
        {icon}
      </span>
      <div className="min-w-0 flex-1">{children}</div>
    </div>
  )
}

function Location({ device, now }: { device: Device; now: number }) {
  const { latitude: lat, longitude: lng } = device
  if (lat === null || lng === null) {
    return (
      <Row icon={<MapPin />}>
        <span className="text-muted-foreground">Location unknown</span>
      </Row>
    )
  }
  return (
    <Row icon={<MapPin />}>
      <div className="flex items-center justify-between gap-2">
        <div className="min-w-0">
          <span dir="auto" className="break-words">
            {formatCoords(lat, lng)}
          </span>
          {device.location_time ? (
            <span className="block text-xs text-muted-foreground">
              {relativeTime(device.location_time, now)}
            </span>
          ) : null}
        </div>
        <a
          href={googleMapsUrl(lat, lng)}
          target="_blank"
          rel="noopener noreferrer"
          aria-label={`Open ${device.full_name} location in Google Maps`}
          className="-my-2 -me-2 inline-flex size-11 shrink-0 items-center justify-center rounded-md text-muted-foreground hover:bg-accent hover:text-accent-foreground focus-visible:ring-[3px] focus-visible:ring-ring focus-visible:outline-none"
        >
          <ExternalLink className="size-4" aria-hidden="true" />
        </a>
      </div>
    </Row>
  )
}

function Battery({ device }: { device: Device }) {
  const level = device.battery_level
  if (level === null) {
    return (
      <Row icon={<BatteryFull />}>
        <span className="text-muted-foreground">Battery unknown</span>
      </Row>
    )
  }
  const pct = Math.max(0, Math.min(100, level))
  const low = pct < 20
  return (
    <Row icon={device.charging ? <BatteryCharging /> : <BatteryFull />}>
      <div className="flex items-center gap-2">
        <div
          aria-hidden="true"
          className="h-2 min-w-0 flex-1 overflow-hidden rounded-full bg-muted"
        >
          <div
            className={cn(
              'h-full rounded-full',
              low ? 'bg-amber-500' : 'bg-green-600 dark:bg-green-500',
            )}
            style={{ width: `${pct}%` }}
          />
        </div>
        <span
          className={cn(
            'shrink-0 font-medium tabular-nums',
            low && 'text-amber-700 dark:text-amber-400',
          )}
        >
          {pct}%
        </span>
        {low ? <span className="sr-only">Low battery</span> : null}
        {device.charging ? (
          <span className="shrink-0 text-xs text-muted-foreground">
            Charging{device.charge_type ? ` · ${device.charge_type}` : ''}
          </span>
        ) : null}
      </div>
    </Row>
  )
}

function WifiStatus({ device }: { device: Device }) {
  if (device.wifi_connected === null) {
    return (
      <Row icon={<Wifi />}>
        <span className="text-muted-foreground">Wi-Fi unknown</span>
      </Row>
    )
  }
  if (!device.wifi_connected) {
    return (
      <Row icon={<WifiOff />}>
        <span>Mobile data</span>
      </Row>
    )
  }
  return (
    <Row icon={<Wifi />}>
      <span>Connected</span>
      {device.ssid ? (
        <>
          {' · '}
          <span dir="auto" className="break-words">
            {device.ssid}
          </span>
        </>
      ) : null}
    </Row>
  )
}

export function DeviceCard({ device, now }: { device: Device; now: number }) {
  const { isAdmin } = useAuth()
  const name = device.full_name || device.device
  const seen =
    device.last_seen > 0 ? `Last seen ${relativeTime(device.last_seen, now)}` : 'Never seen'
  return (
    <Card className="gap-3 p-4" role="article" aria-label={name} data-testid="device-card">
      <div className="flex items-center gap-3">
        <Avatar device={device} />
        <div className="min-w-0 flex-1">
          <h2 dir="auto" className="truncate text-base leading-tight font-semibold" title={name}>
            {name}
          </h2>
          <p className="text-xs text-muted-foreground">{seen}</p>
        </div>
        <OnlineBadge online={device.online} />
      </div>
      <div className="flex flex-col gap-1.5">
        <Location device={device} now={now} />
        <Battery device={device} />
        <WifiStatus device={device} />
      </div>
      {isAdmin ? <DeviceActions device={device} /> : null}
      <div className="mt-auto flex items-center gap-2 border-t pt-2 text-xs text-muted-foreground">
        <Smartphone aria-hidden="true" className="size-3.5 shrink-0" />
        <span className="shrink-0">
          {device.version ? `v${device.version}` : 'Version unknown'}
        </span>
        {device.device_info ? (
          <span dir="auto" className="min-w-0 flex-1 truncate" title={device.device_info}>
            · {device.device_info}
          </span>
        ) : null}
      </div>
    </Card>
  )
}

export function DeviceCardSkeleton() {
  return (
    <Card className="gap-3 p-4" aria-hidden="true">
      <div className="flex items-center gap-3">
        <div className="size-12 animate-pulse rounded-full bg-accent" />
        <div className="flex-1 space-y-2">
          <div className="h-4 w-2/3 animate-pulse rounded bg-accent" />
          <div className="h-3 w-1/3 animate-pulse rounded bg-accent" />
        </div>
      </div>
      <div className="h-4 w-full animate-pulse rounded bg-accent" />
      <div className="h-4 w-4/5 animate-pulse rounded bg-accent" />
      <div className="h-4 w-3/5 animate-pulse rounded bg-accent" />
    </Card>
  )
}
