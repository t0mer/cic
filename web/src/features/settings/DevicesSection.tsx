import { Pencil, Trash2 } from 'lucide-react'
import { useState } from 'react'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { DeviceAvatar } from '@/features/devices/DeviceAvatar'
import { useNow } from '@/lib/now'
import { relativeTime } from '@/lib/time'
import type { Device } from '@/lib/types'
import { cn } from '@/lib/utils'
import { DeviceDeleteDialog } from './DeviceDeleteDialog'
import { DeviceEditDialog } from './DeviceEditDialog'
import { useAllDevices } from './queries'
import { QueryState } from './QueryState'

const byName = (a: Device, b: Device) =>
  (a.full_name || a.device).localeCompare(b.full_name || b.device, undefined, {
    sensitivity: 'base',
    numeric: true,
  }) || a.key.localeCompare(b.key)

function DeviceRow({ device, now }: { device: Device; now: number }) {
  const [editing, setEditing] = useState(false)
  const [deleting, setDeleting] = useState(false)
  const name = device.full_name || device.device
  return (
    <li
      className="flex flex-col gap-3 rounded-lg border p-3 sm:flex-row sm:items-center"
      data-testid="settings-device-row"
    >
      <div className="flex min-w-0 flex-1 items-center gap-3">
        <DeviceAvatar deviceKey={device.key} name={name} color={device.color} />
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-2">
            <span dir="auto" className="truncate font-medium">
              {name}
            </span>
            {device.hidden ? <Badge variant="secondary">Hidden</Badge> : null}
          </div>
          <p className="truncate font-mono text-xs text-muted-foreground">{device.key}</p>
          <p className="flex items-center gap-1.5 text-xs text-muted-foreground">
            <span
              aria-hidden="true"
              className={cn(
                'size-2 rounded-full',
                device.online ? 'bg-green-600 dark:bg-green-400' : 'bg-zinc-500',
              )}
            />
            <span>{device.online ? 'Online' : 'Offline'}</span>
            <span>·</span>
            <span>
              {device.last_seen > 0
                ? `Last seen ${relativeTime(device.last_seen, now)}`
                : 'Never seen'}
            </span>
          </p>
        </div>
      </div>
      <div className="flex gap-1">
        <Button
          variant="outline"
          className="h-11 px-4"
          aria-label={`Edit ${name}`}
          onClick={() => setEditing(true)}
        >
          <Pencil aria-hidden="true" />
          Edit
        </Button>
        <Button
          variant="outline"
          className="h-11 px-4 text-destructive"
          aria-label={`Delete ${name}`}
          onClick={() => setDeleting(true)}
        >
          <Trash2 aria-hidden="true" />
          Delete
        </Button>
      </div>
      <DeviceEditDialog device={device} open={editing} onOpenChange={setEditing} />
      <DeviceDeleteDialog device={device} open={deleting} onOpenChange={setDeleting} />
    </li>
  )
}

export function DevicesSection() {
  const all = useAllDevices()
  const now = useNow()
  return (
    <section aria-labelledby="devices-h" className="flex flex-col gap-4">
      <div>
        <h2 id="devices-h" className="text-lg font-semibold">
          Devices
        </h2>
        <p className="text-sm text-muted-foreground">All phones, including hidden ones.</p>
      </div>
      <QueryState query={all} what="devices" empty="No phones yet">
        {(list) => (
          <ul className="flex flex-col gap-2">
            {[...list].sort(byName).map((d) => (
              <DeviceRow key={d.key} device={d} now={now} />
            ))}
          </ul>
        )}
      </QueryState>
    </section>
  )
}
