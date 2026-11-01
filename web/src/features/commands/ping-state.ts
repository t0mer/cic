import { useSyncExternalStore } from 'react'

export const PING_SENT_MS = 2000
export const PING_STOP_MS = 3000

export interface PingState {
  /** A ping request for this device is in flight (blocks a second send, even across remounts). */
  inFlight: boolean
  /** The "Sent" flash is showing. */
  sent: boolean
  /** The temporary Stop button is showing. */
  stop: boolean
}

const IDLE: PingState = { inFlight: false, sent: false, stop: false }
const states = new Map<string, PingState>()
const timers = new Map<
  string,
  { sent?: ReturnType<typeof setTimeout>; stop?: ReturnType<typeof setTimeout> }
>()
const listeners = new Set<() => void>()

const emit = () => listeners.forEach((l) => l())

function patch(key: string, p: Partial<PingState>) {
  const next = { ...(states.get(key) ?? IDLE), ...p }
  states.set(key, next.inFlight || next.sent || next.stop ? next : IDLE)
  if (!(next.inFlight || next.sent || next.stop)) states.delete(key)
  emit()
}

function arm(key: string, field: 'sent' | 'stop', ms: number) {
  const t = timers.get(key) ?? {}
  clearTimeout(t[field])
  patch(key, { [field]: true })
  t[field] = setTimeout(() => patch(key, { [field]: false }), ms)
  timers.set(key, t)
}

export const pingState = (key: string): PingState => states.get(key) ?? IDLE

/** True when the request was registered; false when one is already in flight. */
export function beginPing(key: string): boolean {
  if (pingState(key).inFlight) return false
  patch(key, { inFlight: true })
  return true
}
export function endPing(key: string) {
  patch(key, { inFlight: false })
}
/** The ack arrived: flash "Sent" and offer Stop for a moment. Survives the sender unmounting. */
export function pingAcked(key: string) {
  arm(key, 'sent', PING_SENT_MS)
  arm(key, 'stop', PING_STOP_MS)
}
export function clearStop(key: string) {
  const t = timers.get(key)
  if (t) clearTimeout(t.stop)
  patch(key, { stop: false })
}
/** Test helper: forget everything. */
export function resetPingState() {
  for (const t of timers.values()) {
    clearTimeout(t.sent)
    clearTimeout(t.stop)
  }
  timers.clear()
  states.clear()
  emit()
}

export function usePingState(key: string): PingState {
  return useSyncExternalStore(
    (l) => {
      listeners.add(l)
      return () => listeners.delete(l)
    },
    () => pingState(key),
  )
}
