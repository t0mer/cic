import { Check, Radio, Square } from 'lucide-react'
import { AlertDialog } from 'radix-ui'
import { useEffect, useRef, useState } from 'react'
import { toast } from 'sonner'

import { Button } from '@/components/ui/button'
import type { Device } from '@/lib/types'
import { actionBtn } from './styles'
import { beginPing, clearStop, endPing, pingAcked, usePingState } from './ping-state'
import { useCommand } from './useCommand'

export { PING_SENT_MS, PING_STOP_MS } from './ping-state'

/**
 * Ping (with the offline "Send anyway" confirmation, the "Sent" flash and the temporary Stop button).
 * Renders a fragment of buttons plus the confirm dialog, so callers place it in their own row.
 * `onConfirmOpenChange(true)` lets a transient host (a hover tooltip) stay open while the dialog is
 * up, a ping is in flight or the Stop button shows. The outcome handling lives in hook-level
 * mutation options and a per-device store, so it survives this component unmounting.
 */
export function PingControl({
  device,
  onConfirmOpenChange,
}: {
  device: Device
  onConfirmOpenChange?: (open: boolean) => void
}) {
  const name = device.full_name || device.device
  const key = device.key
  const ping = useCommand(device, 'ping', {
    onAck: () => {
      pingAcked(key)
      toast.success(`Ping sent to ${name}`)
    },
    onSettled: () => endPing(key),
  })
  const stop = useCommand(device, 'ping/stop', {
    onAck: () => {
      clearStop(key)
      toast.success(`Ping stopped on ${name}`)
    },
  })
  const { inFlight, sent, stop: stopVisible } = usePingState(key)
  const [confirmOffline, setConfirmOffline] = useState(false)

  const hold = confirmOffline || inFlight || stopVisible
  const holdCb = useRef(onConfirmOpenChange)
  useEffect(() => {
    holdCb.current = onConfirmOpenChange
  })
  const lastHold = useRef(false)
  useEffect(() => {
    if (lastHold.current === hold) return
    lastHold.current = hold
    holdCb.current?.(hold)
  }, [hold])
  // Never leave the host held open by a control that is gone.
  useEffect(
    () => () => {
      if (lastHold.current) holdCb.current?.(false)
    },
    [],
  )

  const doPing = () => {
    if (!beginPing(key)) return
    ping.mutate({})
  }

  const offline = !device.online
  const onPing = () => {
    if (offline) setConfirmOffline(true)
    else doPing()
  }

  return (
    <>
      <Button
        variant="outline"
        className={actionBtn}
        onClick={onPing}
        disabled={inFlight}
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
          onClick={() => stop.mutate(undefined)}
          disabled={stop.isPending}
          aria-label={`Stop ping on ${name}`}
        >
          <Square aria-hidden="true" />
          Stop
        </Button>
      ) : null}
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
    </>
  )
}
