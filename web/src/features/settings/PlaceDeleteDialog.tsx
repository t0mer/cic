import { ConfirmDialog } from '@/components/ui/confirm-dialog'
import { commandErrorMessage } from '@/lib/errors'
import type { Place } from '@/lib/types'
import { useDeletePlace, useOnce } from './queries'

export function PlaceDeleteDialog({
  place,
  open,
  onOpenChange,
}: {
  place: Place
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const once = useOnce()
  const del = useDeletePlace(place, () => onOpenChange(false))
  const change = (o: boolean) => {
    if (!o) del.reset()
    onOpenChange(o)
  }
  return (
    <ConfirmDialog
      open={open}
      onOpenChange={change}
      title={`Delete ${place.name}?`}
      description="Phones will show their address again instead of this place name."
      confirmLabel="Delete"
      pendingLabel="Deleting…"
      destructive
      pending={del.isPending}
      error={del.error ? commandErrorMessage(del.error) : null}
      onConfirm={() => once((done) => del.mutate(undefined, { onSettled: done }))}
    />
  )
}
