import { useCallback, useEffect, useRef, useState } from 'react'

export const OPEN_DELAY_MS = 150
export const CLOSE_DELAY_MS = 300

/**
 * Hover-intent for one open tooltip at a time. Call `enter(key)` when the pointer reaches a marker
 * OR its tooltip and `leave()` when it leaves either: the tooltip opens after OPEN_DELAY_MS and
 * closes CLOSE_DELAY_MS after the pointer left both. `show`/`hide` skip the delays (focus, Esc,
 * tap); `setHold(true)` keeps it open (e.g. while a confirm dialog is up).
 */
export function useHoverIntent(openDelay = OPEN_DELAY_MS, closeDelay = CLOSE_DELAY_MS) {
  const [activeKey, setActive] = useState<string | null>(null)
  const activeRef = useRef<string | null>(null)
  const holdRef = useRef(false)
  const openTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined)
  const closeTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined)

  const suppressRef = useRef(false)
  const suppressTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined)

  const set = useCallback((key: string | null) => {
    // A hold belongs to the tooltip that asked for it; it never outlives that tooltip.
    if (key !== activeRef.current) holdRef.current = false
    activeRef.current = key
    setActive(key)
  }, [])
  const clearTimers = useCallback(() => {
    clearTimeout(openTimer.current)
    clearTimeout(closeTimer.current)
  }, [])

  const enter = useCallback(
    (key: string) => {
      clearTimeout(closeTimer.current)
      clearTimeout(openTimer.current)
      if (activeRef.current === key) return
      openTimer.current = setTimeout(() => set(key), openDelay)
    },
    [openDelay, set],
  )

  const leave = useCallback(() => {
    clearTimeout(openTimer.current)
    clearTimeout(closeTimer.current)
    closeTimer.current = setTimeout(() => {
      if (!holdRef.current) set(null)
    }, closeDelay)
  }, [closeDelay, set])

  const show = useCallback(
    (key: string) => {
      clearTimers()
      set(key)
    },
    [clearTimers, set],
  )

  const hide = useCallback(() => {
    clearTimers()
    set(null)
  }, [clearTimers, set])

  /** The marker for `key` is going away: close its tooltip (and drop its hold) if it is the open one. */
  const release = useCallback(
    (key: string) => {
      if (activeRef.current !== key) return
      clearTimers()
      set(null)
    },
    [clearTimers, set],
  )

  /** Focus landed on a marker: open its tooltip unless focus was just restored by us (see below). */
  const focus = useCallback(
    (key: string) => {
      if (!suppressRef.current) show(key)
    },
    [show],
  )

  /** Ignore `focus(key)` for a moment, so moving focus back to a marker after Esc doesn't reopen it. */
  const suppressFocus = useCallback((ms = 150) => {
    suppressRef.current = true
    clearTimeout(suppressTimer.current)
    suppressTimer.current = setTimeout(() => {
      suppressRef.current = false
    }, ms)
  }, [])

  const setHold = useCallback(
    (hold: boolean) => {
      holdRef.current = hold
      if (!hold && activeRef.current !== null) leave()
    },
    [leave],
  )

  useEffect(() => clearTimers, [clearTimers])
  useEffect(() => () => clearTimeout(suppressTimer.current), [])

  return { activeKey, enter, leave, show, hide, release, focus, suppressFocus, setHold }
}
