import { useSyncExternalStore } from 'react'

const TICK_MS = 30_000
const listeners = new Set<() => void>()
let timer: ReturnType<typeof setInterval> | undefined
let current = Math.floor(Date.now() / 1000)

function subscribe(fn: () => void) {
  listeners.add(fn)
  if (listeners.size === 1) {
    current = Math.floor(Date.now() / 1000)
    timer = setInterval(() => {
      current = Math.floor(Date.now() / 1000)
      listeners.forEach((l) => l())
    }, TICK_MS)
  }
  return () => {
    listeners.delete(fn)
    if (listeners.size === 0 && timer !== undefined) {
      clearInterval(timer)
      timer = undefined
    }
  }
}

/** Current unix seconds, refreshed every 30 s by one shared interval. */
export function useNow(): number {
  return useSyncExternalStore(subscribe, () => current)
}
