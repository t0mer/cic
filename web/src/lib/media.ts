import { useSyncExternalStore } from 'react'

function watch(query: string, fallback: boolean) {
  const mq = typeof window !== 'undefined' && window.matchMedia ? window.matchMedia(query) : null
  return {
    subscribe: (fn: () => void) => {
      mq?.addEventListener('change', fn)
      return () => mq?.removeEventListener('change', fn)
    },
    get: () => (mq ? mq.matches : fallback),
  }
}

function useMedia(query: string, fallback: boolean): boolean {
  // matchMedia returns a fresh list per call, so subscribe through a stable closure per render key.
  return useSyncExternalStore(
    (fn) => watch(query, fallback).subscribe(fn),
    () => watch(query, fallback).get(),
    () => fallback,
  )
}

/** True on devices whose primary input can hover (mouse); false on touch. */
export const useCanHover = () => useMedia('(hover: hover)', true)
export const prefersReducedMotion = (): boolean =>
  typeof window !== 'undefined' && !!window.matchMedia?.('(prefers-reduced-motion: reduce)').matches
/** Mobile layout (below Tailwind's md breakpoint). */
export const useIsNarrow = () => useMedia('(max-width: 767.98px)', false)
