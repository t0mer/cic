import { useRef, useState, type FormEvent } from 'react'
import { toast } from 'sonner'

import { Button } from '@/components/ui/button'
import { FieldError, Input, Label, Textarea } from '@/components/ui/field'
import { ResponsiveDialog } from '@/components/ui/responsive-dialog'
import {
  countChars,
  hasForbiddenChars,
  isValidE164,
  maskPhone,
  normalizePhone,
  smsSegments,
} from '@/lib/sms'
import type { Device } from '@/lib/types'
import { useCommand } from './useCommand'

export const SMS_TEXT_MAX = 1600

export function smsTextProblem(text: string): string | null {
  const t = text.trim()
  if (!t) return 'Message is required'
  if (countChars(t) > SMS_TEXT_MAX) return `Message is limited to ${SMS_TEXT_MAX} characters`
  if (hasForbiddenChars(t)) return 'Control and invisible formatting characters are not allowed'
  return null
}

export function SmsCounter({ text }: { text: string }) {
  const info = smsSegments(text.trim())
  return (
    <p className="text-xs text-muted-foreground" data-testid="sms-counter">
      {info.characters} {info.characters === 1 ? 'character' : 'characters'} · {info.segments}{' '}
      {info.segments === 1 ? 'segment' : 'segments'} · {info.encoding}
    </p>
  )
}

/** Characters of the message echoed back in the confirm step. */
export const SMS_PREVIEW_CHARS = 160

export function previewText(text: string): string {
  const chars = Array.from(text)
  return chars.length > SMS_PREVIEW_CHARS ? chars.slice(0, SMS_PREVIEW_CHARS).join('') + '…' : text
}

function Form({
  cmd,
  onSend,
}: {
  cmd: ReturnType<typeof useCommand>
  onSend: (number: string) => void
}) {
  const [phone, setPhone] = useState('')
  const [text, setText] = useState('')
  const [touched, setTouched] = useState(false)
  const [confirming, setConfirming] = useState(false)
  const number = normalizePhone(phone)
  const phoneBad = !isValidE164(number)
  const textProblem = smsTextProblem(text)

  const review = (e: FormEvent) => {
    e.preventDefault()
    setTouched(true)
    if (phoneBad || textProblem) return
    setConfirming(true)
  }

  const send = () => {
    if (cmd.isPending) return
    onSend(number)
    cmd.mutate({ phone: number, text: text.trim() }, { onError: () => setConfirming(false) })
  }

  if (confirming) {
    const info = smsSegments(text.trim())
    return (
      <div className="flex flex-col gap-4">
        <p className="text-base font-medium">Send this SMS to</p>
        <p
          dir="ltr"
          data-testid="sms-confirm-number"
          className="font-mono text-2xl font-semibold tracking-wide break-all"
        >
          {number}
        </p>
        <p
          dir="auto"
          data-testid="sms-confirm-text"
          className="max-h-40 overflow-y-auto rounded-md border bg-muted/40 p-3 text-sm break-words whitespace-pre-wrap"
        >
          {previewText(text.trim())}
        </p>
        <p className="text-sm text-muted-foreground" data-testid="sms-confirm-info">
          {info.segments} {info.segments === 1 ? 'segment' : 'segments'} · {info.encoding}. Messages
          cannot be recalled.
        </p>
        <div className="flex flex-col gap-2 sm:flex-row-reverse">
          <Button className="h-11 sm:flex-1" onClick={send} disabled={cmd.isPending}>
            {cmd.isPending ? 'Sending…' : 'Send SMS'}
          </Button>
          <Button
            variant="outline"
            className="h-11 sm:flex-1"
            onClick={() => setConfirming(false)}
            disabled={cmd.isPending}
          >
            Back
          </Button>
        </div>
      </div>
    )
  }

  return (
    <form onSubmit={review} className="flex flex-col gap-4" noValidate>
      <div className="flex flex-col gap-1.5">
        <Label htmlFor="sms-phone">Phone number</Label>
        <Input
          id="sms-phone"
          type="tel"
          dir="ltr"
          inputMode="tel"
          autoComplete="off"
          placeholder="+972501234567"
          value={phone}
          onChange={(e) => setPhone(e.target.value)}
          aria-invalid={touched && phoneBad ? true : undefined}
          aria-describedby="sms-phone-hint"
        />
        <p
          id="sms-phone-hint"
          role={touched && phoneBad ? 'alert' : undefined}
          className={`text-xs ${touched && phoneBad ? 'text-destructive' : 'text-muted-foreground'}`}
        >
          {touched && phoneBad
            ? 'Enter the number in international format, like +972501234567'
            : 'International format, like +972501234567'}
        </p>
      </div>
      <div className="flex flex-col gap-1.5">
        <Label htmlFor="sms-text">Message</Label>
        <Textarea
          id="sms-text"
          dir="auto"
          required
          value={text}
          onChange={(e) => setText(e.target.value)}
          aria-invalid={touched && textProblem ? true : undefined}
          aria-describedby={touched && textProblem ? 'sms-error' : undefined}
        />
        <SmsCounter text={text} />
      </div>
      {touched && textProblem ? <FieldError id="sms-error">{textProblem}</FieldError> : null}
      <Button type="submit" className="h-11">
        Review
      </Button>
    </form>
  )
}

export function SmsDialog({
  device,
  open,
  onOpenChange,
  onSent,
  onUnknown,
}: {
  device: Device
  open: boolean
  onOpenChange: (open: boolean) => void
  onSent: (id: string) => void
  /** The outcome is unknown: the SMS may still arrive. */
  onUnknown?: () => void
}) {
  const name = device.full_name || device.device
  const sentTo = useRef('')
  // Owned here, not in the form, so the success path survives the form (or dialog) going away.
  const cmd = useCommand(device, 'sms', {
    onAck: (ack) => {
      toast.success(`SMS sent to ${maskPhone(sentTo.current)}`)
      onSent(ack.id)
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
      title="Send SMS"
      description={`Sent from ${name}'s SIM.`}
    >
      <Form cmd={cmd} onSend={(n) => (sentTo.current = n)} />
    </ResponsiveDialog>
  )
}
