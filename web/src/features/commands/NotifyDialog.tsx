import { useState, type FormEvent } from 'react'
import { toast } from 'sonner'

import { Button } from '@/components/ui/button'
import { FieldError, Input, Label, Textarea } from '@/components/ui/field'
import { ResponsiveDialog } from '@/components/ui/responsive-dialog'
import { countChars, hasForbiddenChars } from '@/lib/sms'
import type { Device } from '@/lib/types'
import { useCommand } from './useCommand'

export const NOTIFY_TITLE_MAX = 100
export const NOTIFY_TEXT_MAX = 1000

export function notifyProblem(title: string, text: string): string | null {
  const t = text.trim()
  if (!t) return 'Message is required'
  if (countChars(t) > NOTIFY_TEXT_MAX) return `Message is limited to ${NOTIFY_TEXT_MAX} characters`
  if (countChars(title.trim()) > NOTIFY_TITLE_MAX)
    return `Title is limited to ${NOTIFY_TITLE_MAX} characters`
  if (hasForbiddenChars(title) || hasForbiddenChars(text))
    return 'Control and invisible formatting characters are not allowed'
  return null
}

function Form({ cmd }: { cmd: ReturnType<typeof useCommand> }) {
  const [title, setTitle] = useState('CIC')
  const [text, setText] = useState('')
  const [touched, setTouched] = useState(false)
  const problem = notifyProblem(title, text)

  const submit = (e: FormEvent) => {
    e.preventDefault()
    setTouched(true)
    if (problem || cmd.isPending) return
    cmd.mutate({ title: title.trim() || 'CIC', text: text.trim() })
  }

  const tn = countChars(title)
  const xn = countChars(text)
  return (
    <form onSubmit={submit} className="flex flex-col gap-4" noValidate>
      <div className="flex flex-col gap-1.5">
        <Label htmlFor="notify-title">Title</Label>
        <Input
          id="notify-title"
          dir="auto"
          value={title}
          onChange={(e) => setTitle(e.target.value)}
          autoComplete="off"
        />
        <p
          className={`text-xs ${tn > NOTIFY_TITLE_MAX ? 'text-destructive' : 'text-muted-foreground'}`}
        >
          {tn}/{NOTIFY_TITLE_MAX}
        </p>
      </div>
      <div className="flex flex-col gap-1.5">
        <Label htmlFor="notify-text">Message</Label>
        <Textarea
          id="notify-text"
          dir="auto"
          required
          value={text}
          onChange={(e) => setText(e.target.value)}
          aria-invalid={touched && problem ? true : undefined}
          aria-describedby={touched && problem ? 'notify-error' : undefined}
        />
        <p
          className={`text-xs ${xn > NOTIFY_TEXT_MAX ? 'text-destructive' : 'text-muted-foreground'}`}
        >
          {xn}/{NOTIFY_TEXT_MAX}
        </p>
      </div>
      {touched && problem ? <FieldError id="notify-error">{problem}</FieldError> : null}
      <Button type="submit" className="h-11" disabled={cmd.isPending}>
        {cmd.isPending ? 'Sending…' : 'Send notification'}
      </Button>
    </form>
  )
}

export function NotifyDialog({
  device,
  open,
  onOpenChange,
  onUnknown,
}: {
  device: Device
  open: boolean
  onOpenChange: (open: boolean) => void
  /** The outcome is unknown: the notification may still arrive. */
  onUnknown?: () => void
}) {
  const name = device.full_name || device.device
  // Owned here so the success toast and close still run if the form unmounts mid-request.
  const cmd = useCommand(device, 'notify', {
    onAck: () => {
      toast.success(`Notification sent to ${name}`)
      onOpenChange(false)
    },
    onUnknown: () => {
      onOpenChange(false)
      onUnknown?.()
    },
  })
  return (
    <ResponsiveDialog
      open={open}
      onOpenChange={onOpenChange}
      locked={cmd.isPending}
      title="Send notification"
      description={`Shows a notification on ${name}.`}
    >
      <Form cmd={cmd} />
    </ResponsiveDialog>
  )
}
