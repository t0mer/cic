import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useCallback, useEffect, useMemo, type ReactNode } from 'react'

import { toast } from 'sonner'

import { clearResults } from '@/features/commands/results'
import {
  authKey,
  bumpSession,
  changePassword as apiChangePassword,
  fetchMe,
  login as apiLogin,
  logout as apiLogout,
  setAuthHandlers,
  setCsrfToken,
} from '@/lib/api'
import { AuthContext, type AuthStatus, type AuthValue } from './auth-context'

export function AuthProvider({ children }: { children: ReactNode }) {
  const qc = useQueryClient()
  const me = useQuery({
    queryKey: authKey,
    queryFn: fetchMe,
    retry: false,
    staleTime: 60_000,
  })

  /** Drops everything except the auth query, so no data outlives a session. */
  const wipe = useCallback(() => {
    clearResults()
    qc.removeQueries({ predicate: (q) => q.queryKey[0] !== authKey[0] })
  }, [qc])

  useEffect(() => {
    setAuthHandlers({
      onUnauthorized: () => {
        bumpSession()
        void qc.cancelQueries({ queryKey: authKey })
        setCsrfToken(null)
        qc.setQueryData(authKey, null)
        wipe()
      },
      onPasswordChangeRequired: () => void qc.invalidateQueries({ queryKey: authKey }),
      // A stale CSRF token: re-reading /me hands out the current one.
      onCsrfRejected: () => void qc.invalidateQueries({ queryKey: authKey }),
    })
    return () => setAuthHandlers(null)
  }, [qc, wipe])

  const login = useCallback(
    async (username: string, password: string) => {
      const res = await apiLogin(username, password)
      setCsrfToken(res.csrf_token)
      bumpSession() // a /me that started before this login must not overwrite the new token
      wipe()
      await qc.invalidateQueries({ queryKey: authKey })
    },
    [qc, wipe],
  )

  const logout = useCallback(async () => {
    // Invalidate in-flight /me responses first, then cancel them, before anything is cleared.
    bumpSession()
    await qc.cancelQueries({ queryKey: authKey })
    try {
      await apiLogout()
    } catch {
      toast.warning('Could not sign out on the server; close the browser to be safe')
    } finally {
      setCsrfToken(null)
      qc.setQueryData(authKey, null)
      wipe()
    }
  }, [qc, wipe])

  const changePassword = useCallback(
    async (current: string, next: string) => {
      await apiChangePassword(current, next)
      await qc.invalidateQueries({ queryKey: authKey })
    },
    [qc],
  )

  const user = me.data ?? null
  const status: AuthStatus = me.isPending
    ? 'loading'
    : me.isError && !user
      ? 'error'
      : user
        ? 'authenticated'
        : 'anonymous'

  const value = useMemo<AuthValue>(
    () => ({
      status,
      user,
      isAdmin: status === 'authenticated' && user?.role === 'admin' && !user.must_change_password,
      mustChangePassword: status === 'authenticated' && !!user?.must_change_password,
      error: me.error,
      retry: () => void me.refetch(),
      login,
      logout,
      changePassword,
    }),
    [status, user, me, login, logout, changePassword],
  )

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>
}
