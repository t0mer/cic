import { BellRing, Check, MessageSquareText, MoreVertical, Radio, Square } from 'lucide-react'
import { AlertDialog, DropdownMenu } from 'radix-ui'
import { useCallback, useState } from 'react'
import { toast } from 'sonner'

import { Button } from '@/components/ui/button'
import type { Device } from '@/lib/types'
import { NotifyDialog } from './NotifyDialog'
import { SmsDialog } from './SmsDialog'
import { SMS_TRACKED_MAX, SmsStatusList } from './SmsStatus'
import { useCommand, useFlag } from './useCommand'

export const PING_SENT_MS = 2000
export const PING_STOP_MS = 3000
/** After an unknown outcome the same action stays disabled this long. */
export const UNKNOWN_HOLD_MS = 30_000

const actionBtn = 'h-11 min-w-11 gap-1.5 px-2 aria-disabled:opacity-60'

export function DeviceActions({ device }: { device: Device }) {
  const name = device.full_name || device.device
  const ping = useCommand(device, 'ping')
  const stop = useCommand(device, 'ping/stop')
  const refreshStatus = useCommand(device, 'refresh-status')
  const refreshLocation = useCommand(device, 'refresh-location')
  const [sent, flashSent] = useFlag(PING_SENT_MS)
  const [stopVisible, showStop, hideStop] = useFlag(PING_STOP_MS)
  const [confirmOffline, setConfirmOffline] = useState(false)
  const [notifyOpen, setNotifyOpen] = useState(false)
  const [smsOpen, setSmsOpen] = useState(false)
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

  const doPing = () => {
    if (ping.isPending) return
    ping.mutate(
      {},
      {
        onSuccess: () => {
          flashSent()
          showStop()
          toast.success(`Ping sent to ${name}`)
        },
      },
    )
  }

  const offline = !device.online
  const onPing = () => {
    if (offline) setConfirmOffline(true)
    else doPing()
  }

  return (
    <div className="flex flex-col gap-2" data-testid="device-actions">
      <div className="flex flex-wrap items-center gap-1.5">
        <Button
          variant="outline"
          className={actionBtn}
          onClick={onPing}
          disabled={ping.isPending}
          aria-disabled={offline ? true : undefined}
          aria-describedby={offline ? `offline-${device.key}` : undefined}
          title={offline ? 'Device is offline. You can still send the ping.' : undefined}
          aria-label={`Ping ${name}`}
        >
          {sent ? <Check aria-hidden="true" /> : <Radio aria-hidden="true" />}
          {sent ? 'Sent' : 'Ping'}
        </Button>
        {offline ? (
          <span id={`offline-${device.key}`} className="sr-only">
            Device is offline. Activating asks for confirmation before sending.
          </span>
        ) : null}
        {stopVisible ? (
          <Button
            variant="secondary"
            className={actionBtn}
            onClick={() =>
              stop.mutate(undefined, {
                onSuccess: () => {
                  hideStop()
                  toast.success(`Ping stopped on ${name}`)
                },
              })
            }
            disabled={stop.isPending}
            aria-label={`Stop ping on ${name}`}
          >
            <Square aria-hidden="true" />
            Stop
          </Button>
        ) : null}
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

      <AlertDialog.Root open={confirmOffline} onOpenChange={setConfirmOffline}>
        <AlertDialog.Portal>
          <AlertDialog.Overlay className="fixed inset-0 z-50 bg-black/60" />
          <AlertDialog.Content className="fixed inset-x-0 top-1/2 z-50 mx-auto flex w-[calc(100%-2rem)] max-w-sm -translate-y-1/2 flex-col gap-4 rounded-xl border bg-background p-6 shadow-lg outline-none">
            <AlertDialog.Title className="text-lg font-semibold">
              Device is offline
            </AlertDialog.Title>
            <AlertDialog.Description dir="auto" className="text-sm text-muted-foreground">
              {name} has not been seen recently, so the ping may not arrive. Send it anyway?
            </AlertDialog.Description>
            <div className="flex flex-col gap-2 sm:flex-row-reverse">
              <AlertDialog.Action asChild>
                <Button className="h-11 sm:flex-1" onClick={doPing}>
                  Send anyway
                </Button>
              </AlertDialog.Action>
              <AlertDialog.Cancel asChild>
                <Button variant="outline" className="h-11 sm:flex-1">
                  Cancel
                </Button>
              </AlertDialog.Cancel>
            </div>
          </AlertDialog.Content>
        </AlertDialog.Portal>
      </AlertDialog.Root>

      <NotifyDialog
        device={device}
        open={notifyOpen}
        onOpenChange={setNotifyOpen}
        onUnknown={() => {
          setUnsure('notification')
          holdNotify()
        }}
      />
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
