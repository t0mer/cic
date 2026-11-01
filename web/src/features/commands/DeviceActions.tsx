import { BellRing, MessageSquareText, MoreVertical, Palette, Trash2 } from 'lucide-react'
import { DropdownMenu } from 'radix-ui'
import { useCallback, useState } from 'react'
import { toast } from 'sonner'

import { Button } from '@/components/ui/button'
import { DeviceDeleteDialog } from '@/features/settings/DeviceDeleteDialog'
import { DeviceEditDialog } from '@/features/settings/DeviceEditDialog'
import type { Device } from '@/lib/types'
import { HistoryLink } from '@/features/history/HistoryLink'
import { NotifyDialog } from './NotifyDialog'
import { PingControl } from './PingControl'
import { SmsDialog } from './SmsDialog'
import { SMS_TRACKED_MAX, SmsStatusList } from './SmsStatus'
import { useCommand, useFlag } from './useCommand'

/** After an unknown outcome the same action stays disabled this long. */
export const UNKNOWN_HOLD_MS = 30_000

import { actionBtn } from './styles'

export function DeviceActions({ device }: { device: Device }) {
  const name = device.full_name || device.device
  const refreshStatus = useCommand(device, 'refresh-status')
  const refreshLocation = useCommand(device, 'refresh-location')
  const [notifyOpen, setNotifyOpen] = useState(false)
  const [smsOpen, setSmsOpen] = useState(false)
  const [editOpen, setEditOpen] = useState(false)
  const [deleteOpen, setDeleteOpen] = useState(false)
  const [smsIds, setSmsIds] = useState<string[]>([])
  const [smsHold, holdSms, releaseSms] = useFlag(UNKNOWN_HOLD_MS)
  const [notifyHold, holdNotify, releaseNotify] = useFlag(UNKNOWN_HOLD_MS)
  const [unsure, setUnsure] = useState<'sms' | 'notification' | null>(null)

  const addSms = useCallback(
    (id: string) => setSmsIds((l) => [id, ...l.filter((x) => x !== id)].slice(0, SMS_TRACKED_MAX)),
    [],
  )
  const removeSms = useCallback((id: string) => setSmsIds((l) => l.filter((x) => x !== id)), [])
  const acknowledge = () => {
    setUnsure(null)
    releaseSms()
    releaseNotify()
  }

  return (
    <div className="flex flex-col gap-2" data-testid="device-actions">
      <div className="flex flex-wrap items-center gap-1.5">
        <PingControl device={device} />
        <HistoryLink device={device} />
        <Button
          variant="outline"
          className={actionBtn}
          onClick={() => setNotifyOpen(true)}
          disabled={notifyHold}
          aria-describedby={notifyHold ? `unsure-${device.key}` : undefined}
          aria-label={`Notify ${name}`}
        >
          <BellRing aria-hidden="true" />
          Notify
        </Button>
        <Button
          variant="outline"
          className={actionBtn}
          onClick={() => setSmsOpen(true)}
          disabled={smsHold}
          aria-describedby={smsHold ? `unsure-${device.key}` : undefined}
          aria-label={`Send SMS from ${name}`}
        >
          <MessageSquareText aria-hidden="true" />
          SMS
        </Button>
        <DropdownMenu.Root>
          <DropdownMenu.Trigger asChild>
            <Button
              variant="ghost"
              className="ms-auto size-11"
              aria-label={`More actions for ${name}`}
            >
              <MoreVertical aria-hidden="true" />
            </Button>
          </DropdownMenu.Trigger>
          <DropdownMenu.Portal>
            <DropdownMenu.Content
              align="end"
              sideOffset={4}
              className="z-50 min-w-48 rounded-md border bg-popover p-1 text-popover-foreground shadow-md"
            >
              <DropdownMenu.Item
                disabled={refreshStatus.isPending}
                onSelect={() =>
                  refreshStatus.mutate(undefined, {
                    onSuccess: () => toast.success(`Status refresh requested from ${name}`),
                  })
                }
                className="flex h-11 cursor-default items-center rounded-sm px-3 text-sm outline-none data-[disabled]:opacity-50 data-[highlighted]:bg-accent"
              >
                Refresh status
              </DropdownMenu.Item>
              <DropdownMenu.Item
                disabled={refreshLocation.isPending}
                onSelect={() =>
                  refreshLocation.mutate(undefined, {
                    onSuccess: () => toast.success(`Location refresh requested from ${name}`),
                  })
                }
                className="flex h-11 cursor-default items-center rounded-sm px-3 text-sm outline-none data-[disabled]:opacity-50 data-[highlighted]:bg-accent"
              >
                Refresh location
              </DropdownMenu.Item>
              <DropdownMenu.Separator className="my-1 h-px bg-border" />
              <DropdownMenu.Item
                onSelect={() => setEditOpen(true)}
                className="flex h-11 cursor-default items-center gap-2 rounded-sm px-3 text-sm outline-none data-[highlighted]:bg-accent"
              >
                <Palette className="size-4" aria-hidden="true" />
                Edit name/color…
              </DropdownMenu.Item>
              <DropdownMenu.Item
                onSelect={() => setDeleteOpen(true)}
                className="flex h-11 cursor-default items-center gap-2 rounded-sm px-3 text-sm text-destructive outline-none data-[highlighted]:bg-accent"
              >
                <Trash2 className="size-4" aria-hidden="true" />
                Delete…
              </DropdownMenu.Item>
            </DropdownMenu.Content>
          </DropdownMenu.Portal>
        </DropdownMenu.Root>
      </div>

      {unsure ? (
        <div
          id={`unsure-${device.key}`}
          role="alert"
          data-testid="unsure-notice"
          className="flex flex-col gap-2 rounded-md border border-amber-500/60 bg-amber-50 p-3 text-sm text-amber-950 dark:bg-amber-950/40 dark:text-amber-100"
        >
          <p dir="auto">
            The {unsure} may still arrive on {name}. Do not resend it. Sending is disabled for 30
            seconds.
          </p>
          <Button variant="outline" className="h-11 self-start px-4" onClick={acknowledge}>
            I understand
          </Button>
        </div>
      ) : null}

      <SmsStatusList ids={smsIds} onFinished={removeSms} />

      <NotifyDialog
        device={device}
        open={notifyOpen}
        onOpenChange={setNotifyOpen}
        onUnknown={() => {
          setUnsure('notification')
          holdNotify()
        }}
      />
      <DeviceEditDialog device={device} open={editOpen} onOpenChange={setEditOpen} />
      <DeviceDeleteDialog device={device} open={deleteOpen} onOpenChange={setDeleteOpen} />
      <SmsDialog
        device={device}
        open={smsOpen}
        onOpenChange={setSmsOpen}
        onSent={addSms}
        onUnknown={() => {
          setUnsure('sms')
          holdSms()
        }}
      />
    </div>
  )
}
