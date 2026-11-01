import { ConfirmDialog } from '@/components/ui/confirm-dialog'
import { commandErrorMessage } from '@/lib/errors'
import type { Device } from '@/lib/types'
import { useDeleteDevice, useOnce } from './queries'

export function DeviceDeleteDialog({
  device,
  open,
  onOpenChange,
}: {
  device: Device
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const name = device.full_name || device.device
  const once = useOnce()
  const del = useDeleteDevice(device, () => onOpenChange(false))
  const change = (o: boolean) => {
    if (!o) del.reset()
    onOpenChange(o)
  }
  return (
    <ConfirmDialog
      open={open}
      onOpenChange={change}
      title={`Delete ${name}?`}
      description={
        <>
          This removes{' '}
          <span dir="auto" className="font-medium">
            {name}
          </span>{' '}
          ({device.key}) and its stored data. It will reappear if the phone publishes again.
        </>
      }
      confirmLabel="Delete"
      pendingLabel="Deleting…"
      destructive
      pending={del.isPending}
      error={del.error ? commandErrorMessage(del.error) : null}
      onConfirm={() => once((done) => del.mutate(undefined, { onSettled: done }))}
    />
  )
}
