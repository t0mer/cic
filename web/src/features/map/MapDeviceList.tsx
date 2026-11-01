import { BatteryFull, BatteryLow, Search } from 'lucide-react'

import { filterDevices, sortDevices } from '@/lib/device-list'
import { describeLocation, hasLocation } from '@/lib/location'
import type { Device } from '@/lib/types'
import { cn } from '@/lib/utils'

/** Devices for the map side list: visible ones matching the search, sorted by name. */
export function listDevices(devices: Device[], query: string): Device[] {
  return sortDevices(filterDevices(devices, query, 'all'), 'name')
}

function Row({ device, onSelect }: { device: Device; onSelect: (key: string) => void }) {
  const name = device.full_name || device.device
  const located = hasLocation(device)
  const level = device.battery_level
  const Battery = level !== null && level < 20 ? BatteryLow : BatteryFull
  // Same wording as the card: name, else coordinates, else nothing invented.
  const view = describeLocation(device, false, 0)
  const where =
    view.kind === 'named' ? view.name : view.kind === 'unknown' ? 'No location' : view.coords
  const place = view.kind !== 'unknown' && view.place ? `${view.place} · ${where}` : where
  const body = (
    <>
      <span
        aria-hidden="true"
        className={cn(
          'mt-1.5 size-2.5 shrink-0 rounded-full',
          device.online ? 'bg-green-600 dark:bg-green-400' : 'bg-zinc-500',
        )}
      />
      <span className="min-w-0 flex-1 text-start">
        <span dir="auto" className="block truncate text-sm font-medium">
          {name}
        </span>
        <span dir="auto" className="block truncate text-xs text-muted-foreground">
          {place}
        </span>
      </span>
      <span className="flex shrink-0 items-center gap-1 pt-0.5 text-xs text-muted-foreground tabular-nums">
        <Battery aria-hidden="true" className="size-3.5" />
        {level === null ? '—' : `${level}%`}
        <span className="sr-only">{device.online ? ', online' : ', offline'}</span>
      </span>
    </>
  )
  return (
    <li>
      {located ? (
        <button
          type="button"
          onClick={() => onSelect(device.key)}
          aria-label={`Show ${name} on the map`}
          className="flex min-h-11 w-full items-start gap-2.5 rounded-md px-3 py-2 hover:bg-accent focus-visible:ring-[3px] focus-visible:ring-ring focus-visible:outline-none"
        >
          {body}
        </button>
      ) : (
        <div className="flex min-h-11 w-full items-start gap-2.5 px-3 py-2 opacity-80">{body}</div>
      )}
    </li>
  )
}

export function MapDeviceList({
  devices,
  query,
  onQuery,
  onSelect,
}: {
  devices: Device[]
  query: string
  onQuery: (q: string) => void
  onSelect: (key: string) => void
}) {
  const shown = listDevices(devices, query)
  return (
    <div className="flex min-h-0 flex-1 flex-col gap-2">
      <div className="relative px-1">
        <Search
          aria-hidden="true"
          className="pointer-events-none absolute start-4 top-1/2 size-4 -translate-y-1/2 text-muted-foreground"
        />
        <input
          type="search"
          value={query}
          onChange={(e) => onQuery(e.target.value)}
          aria-label="Search phones"
          placeholder="Search phones"
          className="h-11 w-full rounded-md border bg-background ps-9 pe-3 text-sm outline-none focus-visible:ring-[3px] focus-visible:ring-ring"
        />
      </div>
      {shown.length === 0 ? (
        <p className="px-3 py-4 text-center text-sm text-muted-foreground">No phones match</p>
      ) : (
        <ul className="min-h-0 flex-1 overflow-y-auto" data-testid="map-device-list">
          {shown.map((d) => (
            <Row key={d.key} device={d} onSelect={onSelect} />
          ))}
        </ul>
      )}
    </div>
  )
}
