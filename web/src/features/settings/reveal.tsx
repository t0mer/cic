import { createContext, useCallback, useContext, useEffect, useMemo, useState } from 'react'
import type { ReactNode } from 'react'

import { useAuth } from '@/features/auth/auth-context'
import { PasswordRevealDialog } from './PasswordRevealDialog'

interface Reveal {
  username: string
  password: string
}

interface RevealApi {
  /** Shows a one-time password. Safe to call after the calling component has unmounted. */
  show: (username: string, password: string) => void
}

const RevealContext = createContext<RevealApi | null>(null)

/**
 * Owns the one-time password dialog above the router outlet, so tab changes, back/forward and
 * route changes cannot drop a secret that is shown only once. Plain React state: never a
 * query/mutation cache entry and never any storage.
 */
export function RevealProvider({ children }: { children: ReactNode }) {
  const { user } = useAuth()
  const [reveal, setReveal] = useState<Reveal | null>(null)
  const show = useCallback(
    (username: string, password: string) => setReveal({ username, password }),
    [],
  )
  const api = useMemo(() => ({ show }), [show])

  // Logout, a 401 or a different account: the secret goes with the session.
  const who = user?.username ?? null
  const [seenWho, setSeenWho] = useState(who)
  if (seenWho !== who) {
    setSeenWho(who)
    setReveal(null)
  }

  const open = reveal !== null
  useEffect(() => {
    if (!open) return
    const guard = (e: BeforeUnloadEvent) => {
      e.preventDefault()
      e.returnValue = ''
    }
    window.addEventListener('beforeunload', guard)
    return () => window.removeEventListener('beforeunload', guard)
  }, [open])

  return (
    <RevealContext.Provider value={api}>
      {children}
      {reveal ? (
        <PasswordRevealDialog
          username={reveal.username}
          password={reveal.password}
          onClose={() => setReveal(null)}
        />
      ) : null}
    </RevealContext.Provider>
  )
}

export function useReveal(): RevealApi {
  const ctx = useContext(RevealContext)
  if (!ctx) throw new Error('RevealProvider missing')
  return ctx
}
