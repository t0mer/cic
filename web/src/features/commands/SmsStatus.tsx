import { useEffect, useRef, useState } from 'react'

import { cn } from '@/lib/utils'
import { useCommandResult } from './results'

export const SMS_TIMEOUT_MS = 60_000
const LINGER_MS = 15_000

/** Pending / final state of one SMS, tied to its command id and fed by SSE `command.result`. */
export function SmsStatus({ id, onFinished }: { id: string; onFinished?: (id: string) => void }) {
  const result = useCommandResult(id)
  // Remember WHICH id timed out, so a new id never inherits the previous one's state.
  const [timedOutFor, setTimedOutFor] = useState<string | null>(null)
  const timedOut = timedOutFor === id
  const finished = useRef(onFinished)
  useEffect(() => {
    finished.current = onFinished
  }, [onFinished])

  useEffect(() => {
    const t = setTimeout(() => setTimedOutFor(id), SMS_TIMEOUT_MS)
    return () => clearTimeout(t)
  }, [id])

  const final = result !== undefined || timedOut
  useEffect(() => {
    if (!final) return
    const t = setTimeout(() => finished.current?.(id), LINGER_MS)
    return () => clearTimeout(t)
  }, [final, id])

  let text = 'Sending SMS…'
  let tone = 'text-muted-foreground'
  if (result?.status === 'confirmed_ok') {
    text = 'Delivered'
    tone = 'text-green-800 dark:text-green-300'
  } else if (result?.status === 'failed') {
    text = result.error ? `SMS failed: ${result.error}` : 'SMS failed'
    tone = 'text-red-800 dark:text-red-300'
  } else if (result?.status === 'no_confirmation' || timedOut) {
    text = 'Sent, no confirmation'
    tone = 'text-amber-800 dark:text-amber-300'
  }
  return (
    <p
      role="status"
      data-testid="sms-status"
      dir="auto"
      className={cn('text-sm font-medium break-words', tone)}
    >
      {text}
      {result?.phone_masked ? ` (${result.phone_masked})` : ''}
    </p>
  )
}

/** Latest N pending/finished SMS of one device, each tracked by its own id. */
export const SMS_TRACKED_MAX = 3

export function SmsStatusList({
  ids,
  onFinished,
}: {
  ids: string[]
  onFinished: (id: string) => void
}) {
  if (ids.length === 0) return null
  return (
    <div className="flex flex-col gap-1">
      {ids.map((id) => (
        <SmsStatus key={id} id={id} onFinished={onFinished} />
      ))}
    </div>
  )
}
