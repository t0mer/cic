import { useSyncExternalStore } from 'react'

import { toast } from 'sonner'

import { actionLabel } from '@/lib/actions'
import type { CommandResult } from '@/lib/types'

const MAX = 200
const results = new Map<string, CommandResult>()
const listeners = new Set<() => void>()

const emit = () => listeners.forEach((l) => l())

/** Stores a `command.result` SSE payload. Results may arrive before the 202 ack, so they are kept by id. */
export function publishResult(r: CommandResult) {
  const id = String(r.id)
  results.delete(id)
  results.set(id, r)
  while (results.size > MAX) {
    const oldest = results.keys().next().value
    if (oldest === undefined) break
    results.delete(oldest)
  }
  emit()
}

export function clearResults() {
  issued.clear()
  if (results.size === 0) return
  results.clear()
  emit()
}

// Ids of commands this session issued (from their 202 acks), bounded like the results.
const issued = new Set<string>()

/** Failure toast for a non-SMS result (SMS failures show inline in SmsStatus). */
export function toastFailure(r: CommandResult) {
  if (r.status !== 'failed' || r.action === 'sms') return
  const label = actionLabel(r.action)
  toast.error(r.error ? `${label} failed: ${r.error}` : `${label} failed`)
}

export const isIssued = (id: string) => issued.has(String(id))

/**
 * Remembers an id this session issued. A result that raced ahead of the ack is surfaced here,
 * because the stream could not yet tell it was ours.
 */
export function registerIssued(id: string) {
  issued.delete(id)
  issued.add(id)
  while (issued.size > MAX) {
    const oldest = issued.values().next().value
    if (oldest === undefined) break
    issued.delete(oldest)
  }
  const early = results.get(id)
  if (early) toastFailure(early)
}

const subscribe = (fn: () => void) => {
  listeners.add(fn)
  return () => {
    listeners.delete(fn)
  }
}

export function useCommandResult(id: string | null): CommandResult | undefined {
  return useSyncExternalStore(subscribe, () => (id === null ? undefined : results.get(id)))
}

/** Test helper: read a stored result without React. */
export const getResultForTest = (id: string) => results.get(id)
