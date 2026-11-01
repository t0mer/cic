import { AlertDialog } from 'radix-ui'
import type { ReactNode } from 'react'

import { Button } from '@/components/ui/button'
import { FieldError } from '@/components/ui/field'

/**
 * Confirmation prompt. While `pending` it cannot be dismissed (Esc, Cancel), so a request that is
 * in flight is never abandoned by accident. The confirm button never closes it by itself.
 */
export function ConfirmDialog({
  open,
  onOpenChange,
  title,
  description,
  confirmLabel,
  pendingLabel,
  destructive = false,
  pending = false,
  error,
  onConfirm,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  title: string
  description: ReactNode
  confirmLabel: string
  pendingLabel?: string
  destructive?: boolean
  pending?: boolean
  error?: string | null
  onConfirm: () => void
}) {
  return (
    <AlertDialog.Root
      open={open}
      onOpenChange={(o) => (pending && !o ? undefined : onOpenChange(o))}
    >
      <AlertDialog.Portal>
        <AlertDialog.Overlay className="fixed inset-0 z-50 bg-black/60 data-[state=closed]:animate-out data-[state=closed]:fade-out-0 data-[state=open]:animate-in data-[state=open]:fade-in-0" />
        <AlertDialog.Content
          data-testid="confirm-dialog"
          onEscapeKeyDown={(e) => pending && e.preventDefault()}
          className="fixed inset-x-0 bottom-0 z-50 flex max-h-[92dvh] flex-col gap-4 overflow-y-auto rounded-t-xl border border-b-0 bg-background p-4 pb-[max(1rem,env(safe-area-inset-bottom))] text-foreground shadow-lg outline-none md:inset-x-0 md:top-1/2 md:bottom-auto md:mx-auto md:w-[calc(100%-2rem)] md:max-w-md md:-translate-y-1/2 md:rounded-xl md:border md:p-6"
        >
          <div>
            <AlertDialog.Title className="text-lg font-semibold">{title}</AlertDialog.Title>
            <AlertDialog.Description asChild>
              <div dir="auto" className="mt-1 text-sm text-muted-foreground">
                {description}
              </div>
            </AlertDialog.Description>
          </div>
          {error ? <FieldError id="confirm-error">{error}</FieldError> : null}
          <div className="flex flex-col-reverse gap-2 sm:flex-row sm:justify-end">
            <AlertDialog.Cancel asChild>
              <Button variant="outline" className="h-11 px-5" disabled={pending}>
                Cancel
              </Button>
            </AlertDialog.Cancel>
            <Button
              variant={destructive ? 'destructive' : 'default'}
              className="h-11 px-5"
              disabled={pending}
              onClick={() => {
                if (!pending) onConfirm()
              }}
            >
              {pending ? (pendingLabel ?? 'Working…') : confirmLabel}
            </Button>
          </div>
        </AlertDialog.Content>
      </AlertDialog.Portal>
    </AlertDialog.Root>
  )
}
