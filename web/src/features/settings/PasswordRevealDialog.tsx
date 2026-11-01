import { Copy, TriangleAlert } from 'lucide-react'
import { useRef, useState } from 'react'
import { toast } from 'sonner'

import { Button } from '@/components/ui/button'
import { CheckboxField } from '@/components/ui/switch-field'
import { ResponsiveDialog } from '@/components/ui/responsive-dialog'

/** Clipboard API where available, a selection + execCommand fallback elsewhere (plain http). */
export async function copyText(text: string, fallbackEl: HTMLElement | null): Promise<boolean> {
  try {
    if (navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(text)
      return true
    }
  } catch {
    /* fall through to the selection fallback */
  }
  if (!fallbackEl) return false
  const range = document.createRange()
  range.selectNodeContents(fallbackEl)
  const sel = window.getSelection()
  sel?.removeAllRanges()
  sel?.addRange(range)
  try {
    return document.execCommand('copy')
  } catch {
    return false
  }
}

/**
 * Shows a one-time password. It cannot be dismissed until the admin confirms they saved it.
 * The password lives only in the props of this component: no storage, URL, log or query cache.
 */
export function PasswordRevealDialog({
  username,
  password,
  onClose,
}: {
  username: string
  password: string
  onClose: () => void
}) {
  const [saved, setSaved] = useState(false)
  const box = useRef<HTMLElement>(null)
  const copy = async () => {
    const ok = await copyText(password, box.current)
    if (ok) toast.success('Password copied')
    else toast.error('Could not copy. Select the password and copy it manually.')
  }
  return (
    <ResponsiveDialog
      open
      onOpenChange={(o) => {
        if (!o) onClose()
      }}
      locked={!saved}
      title="Save this password now"
      description={`Temporary password for ${username}.`}
    >
      <div
        role="alert"
        className="flex gap-2 rounded-md border border-amber-500/60 bg-amber-50 p-3 text-sm text-amber-950 dark:bg-amber-950/40 dark:text-amber-100"
      >
        <TriangleAlert className="mt-0.5 size-4 shrink-0" aria-hidden="true" />
        <p>This password will not be shown again. {username} must change it at the next sign-in.</p>
      </div>
      <div className="flex items-stretch gap-2">
        <code
          ref={box}
          data-testid="one-time-password"
          dir="ltr"
          className="flex min-h-11 min-w-0 flex-1 items-center rounded-md border bg-muted px-3 font-mono text-base break-all select-all"
        >
          {password}
        </code>
        <Button
          type="button"
          variant="outline"
          className="h-auto min-h-11 px-4"
          onClick={() => void copy()}
        >
          <Copy aria-hidden="true" />
          Copy
        </Button>
      </div>
      <p className="text-xs text-muted-foreground">
        Your clipboard may keep a copy until you copy something else.
      </p>
      <p className="text-sm text-muted-foreground">If you lose this, reset the password again.</p>
      <CheckboxField label="I have saved it" checked={saved} onChange={setSaved} />
      <Button type="button" className="h-11" disabled={!saved} onClick={onClose}>
        Close
      </Button>
    </ResponsiveDialog>
  )
}
