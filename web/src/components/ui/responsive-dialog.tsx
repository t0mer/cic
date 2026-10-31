import { X } from 'lucide-react'
import { Dialog } from 'radix-ui'
import type { ReactNode } from 'react'

/** Centered dialog from md up, bottom sheet below. */
export function ResponsiveDialog({
  open,
  onOpenChange,
  title,
  description,
  locked = false,
  children,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  title: string
  description: string
  /** While true the dialog cannot be dismissed (Esc, overlay, X), e.g. while a request is in flight. */
  locked?: boolean
  children: ReactNode
}) {
  return (
    <Dialog.Root open={open} onOpenChange={(o) => (locked && !o ? undefined : onOpenChange(o))}>
      <Dialog.Portal>
        <Dialog.Overlay className="fixed inset-0 z-50 bg-black/60 data-[state=closed]:animate-out data-[state=closed]:fade-out-0 data-[state=open]:animate-in data-[state=open]:fade-in-0" />
        <Dialog.Content
          data-testid="dialog"
          onEscapeKeyDown={(e) => locked && e.preventDefault()}
          onPointerDownOutside={(e) => locked && e.preventDefault()}
          onInteractOutside={(e) => locked && e.preventDefault()}
          className="fixed inset-x-0 bottom-0 z-50 flex max-h-[92dvh] flex-col gap-4 overflow-y-auto rounded-t-xl border border-b-0 bg-background p-4 pb-[max(1rem,env(safe-area-inset-bottom))] text-foreground shadow-lg outline-none data-[state=closed]:animate-out data-[state=closed]:slide-out-to-bottom data-[state=open]:animate-in data-[state=open]:slide-in-from-bottom md:inset-x-0 md:top-1/2 md:bottom-auto md:mx-auto md:w-[calc(100%-2rem)] md:max-w-lg md:-translate-y-1/2 md:rounded-xl md:border md:p-6 md:data-[state=closed]:slide-out-to-bottom-0 md:data-[state=open]:slide-in-from-bottom-0"
        >
          <div className="flex items-start gap-2">
            <div className="min-w-0 flex-1">
              <Dialog.Title className="text-lg font-semibold">{title}</Dialog.Title>
              <Dialog.Description dir="auto" className="text-sm text-muted-foreground">
                {description}
              </Dialog.Description>
            </div>
            <Dialog.Close
              aria-label="Close"
              disabled={locked}
              className="-m-2 inline-flex size-11 shrink-0 items-center justify-center rounded-md hover:bg-accent disabled:opacity-40 focus-visible:ring-[3px] focus-visible:ring-ring focus-visible:outline-none"
            >
              <X className="size-5" aria-hidden="true" />
            </Dialog.Close>
          </div>
          {children}
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  )
}
