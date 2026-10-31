import type { ReactNode } from 'react'

import { Button } from '@/components/ui/button'
import { AuthShell } from './AuthShell'
import { useAuth } from './auth-context'
import { ChangePasswordPage } from './ChangePasswordPage'
import { LoginPage } from './LoginPage'

export function AuthGate({ children }: { children: ReactNode }) {
  const { status, mustChangePassword, retry, error } = useAuth()
  if (status === 'loading')
    return (
      <div
        role="status"
        className="flex min-h-dvh items-center justify-center text-muted-foreground"
      >
        Loading…
      </div>
    )
  if (status === 'error')
    return (
      <AuthShell title="Cannot reach the server">
        <p role="alert" className="mb-4 text-sm text-muted-foreground">
          {error?.message}
        </p>
        <Button className="h-11 w-full" onClick={retry}>
          Retry
        </Button>
      </AuthShell>
    )
  if (status === 'anonymous') return <LoginPage />
  if (mustChangePassword) return <ChangePasswordPage />
  return <>{children}</>
}
