import { useSyncExternalStore } from 'react'

const root = () => document.documentElement

function subscribe(fn: () => void) {
  const obs = new MutationObserver(fn)
  obs.observe(root(), { attributes: true, attributeFilter: ['class'] })
  return () => obs.disconnect()
}

/** Whether the app is currently dark, following the `dark` class the ThemeProvider sets. */
export function useIsDark(): boolean {
  return useSyncExternalStore(
    subscribe,
    () => root().classList.contains('dark'),
    () => false,
  )
}
