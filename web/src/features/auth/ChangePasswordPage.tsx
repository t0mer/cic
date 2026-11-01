import { useEffect, useRef, useState, type FormEvent } from 'react'
import { toast } from 'sonner'

import { Button } from '@/components/ui/button'
import { FieldError, Input, Label } from '@/components/ui/field'
import { ApiError } from '@/lib/api'
import { MIN_PASSWORD } from '@/lib/validation'
import { AuthShell } from './AuthShell'
import { useAuth } from './auth-context'

export { MIN_PASSWORD }

/** Client-side check is UX only; the server enforces policy. */
export function passwordProblem(next: string, confirm: string): string | null {
  if (Array.from(next).length < MIN_PASSWORD)
    return `New password must be at least ${MIN_PASSWORD} characters`
  if (next !== confirm) return 'Passwords do not match'
  return null
}

export function serverMessage(err: unknown): string {
  if (err instanceof ApiError) {
    if (err.status === 401) return 'Current password is incorrect'
    if (err.status === 429)
      return err.retryAfter !== null
        ? `Too many attempts. Try again in ${err.retryAfter}s.`
        : 'Too many attempts. Try again later.'
    if (err.status >= 400 && err.status < 500) return err.message
  }
  return 'Could not change the password. Try again.'
}

export function ChangePasswordPage() {
  const { changePassword, logout, user } = useAuth()
  const [current, setCurrent] = useState('')
  const [next, setNext] = useState('')
  const [confirm, setConfirm] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const ref = useRef<HTMLInputElement>(null)

  useEffect(() => {
    ref.current?.focus()
  }, [])

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (busy) return
    const problem = passwordProblem(next, confirm)
    if (problem) {
      setError(problem)
      return
    }
    setBusy(true)
    setError(null)
    try {
      await changePassword(current, next)
      toast.success('Password changed')
    } catch (err) {
      setError(serverMessage(err))
      setBusy(false)
    }
  }

  return (
    <AuthShell title="Choose a new password">
      <p className="mb-4 text-sm text-muted-foreground">
        {user ? `Signed in as ${user.username}. ` : ''}You must change your password before
        continuing.
      </p>
      <form onSubmit={(e) => void submit(e)} className="flex flex-col gap-4" noValidate>
        <input
          type="text"
          name="username"
          autoComplete="username"
          value={user?.username ?? ''}
          readOnly
          hidden
        />
        <div className="flex flex-col gap-1.5">
          <Label htmlFor="cp-current">Current password</Label>
          <Input
            id="cp-current"
            ref={ref}
            type="password"
            autoComplete="current-password"
            required
            value={current}
            onChange={(e) => setCurrent(e.target.value)}
            disabled={busy}
          />
        </div>
        <div className="flex flex-col gap-1.5">
          <Label htmlFor="cp-new">New password</Label>
          <Input
            id="cp-new"
            type="password"
            autoComplete="new-password"
            required
            value={next}
            onChange={(e) => setNext(e.target.value)}
            disabled={busy}
            aria-describedby="cp-hint"
          />
          <p id="cp-hint" className="text-xs text-muted-foreground">
            At least {MIN_PASSWORD} characters.
          </p>
        </div>
        <div className="flex flex-col gap-1.5">
          <Label htmlFor="cp-confirm">Confirm new password</Label>
          <Input
            id="cp-confirm"
            type="password"
            autoComplete="new-password"
            required
            value={confirm}
            onChange={(e) => setConfirm(e.target.value)}
            disabled={busy}
            aria-describedby={error ? 'cp-error' : undefined}
          />
        </div>
        {error ? <FieldError id="cp-error">{error}</FieldError> : null}
        <Button type="submit" className="h-11" disabled={busy || !current || !next || !confirm}>
          {busy ? 'Saving…' : 'Change password'}
        </Button>
        <Button
          type="button"
          variant="ghost"
          className="h-11"
          onClick={() => void logout()}
          disabled={busy}
        >
          Sign out
        </Button>
      </form>
    </AuthShell>
  )
}
