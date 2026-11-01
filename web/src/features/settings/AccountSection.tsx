import { LogOut } from 'lucide-react'
import { useState, type FormEvent } from 'react'
import { toast } from 'sonner'

import { Button } from '@/components/ui/button'
import { FieldError, Input, Label } from '@/components/ui/field'
import { passwordProblem, serverMessage } from '@/features/auth/ChangePasswordPage'
import { confirmLeave } from '@/lib/leave-guard'
import { useAuth } from '@/features/auth/auth-context'
import { MIN_PASSWORD } from '@/lib/validation'
import { TwoFactorSection } from './TwoFactorSection'

function PasswordForm() {
  const { changePassword } = useAuth()
  const [current, setCurrent] = useState('')
  const [next, setNext] = useState('')
  const [confirm, setConfirm] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

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
      setCurrent('')
      setNext('')
      setConfirm('')
      toast.success('Password changed')
    } catch (err) {
      setError(serverMessage(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <form
      onSubmit={(e) => void submit(e)}
      className="flex max-w-md flex-col gap-4 rounded-lg border p-4"
      noValidate
      aria-labelledby="acct-pw-h"
    >
      <h3 id="acct-pw-h" className="text-sm font-medium">
        Change password
      </h3>
      <div className="flex flex-col gap-1.5">
        <Label htmlFor="acct-current">Current password</Label>
        <Input
          id="acct-current"
          type="password"
          autoComplete="current-password"
          value={current}
          onChange={(e) => setCurrent(e.target.value)}
          disabled={busy}
        />
      </div>
      <div className="flex flex-col gap-1.5">
        <Label htmlFor="acct-new">New password</Label>
        <Input
          id="acct-new"
          type="password"
          autoComplete="new-password"
          value={next}
          onChange={(e) => setNext(e.target.value)}
          disabled={busy}
          aria-describedby="acct-hint"
        />
        <p id="acct-hint" className="text-xs text-muted-foreground">
          At least {MIN_PASSWORD} characters.
        </p>
      </div>
      <div className="flex flex-col gap-1.5">
        <Label htmlFor="acct-confirm">Confirm new password</Label>
        <Input
          id="acct-confirm"
          type="password"
          autoComplete="new-password"
          value={confirm}
          onChange={(e) => setConfirm(e.target.value)}
          disabled={busy}
        />
      </div>
      {error ? <FieldError id="acct-error">{error}</FieldError> : null}
      <Button type="submit" className="h-11" disabled={busy || !current || !next || !confirm}>
        {busy ? 'Saving…' : 'Change password'}
      </Button>
    </form>
  )
}

export function AccountSection() {
  const { user, logout } = useAuth()
  return (
    <section aria-labelledby="acct-h" className="flex flex-col gap-4">
      <h2 id="acct-h" className="text-lg font-semibold">
        Account
      </h2>
      <dl className="grid max-w-md gap-3 rounded-lg border p-4 sm:grid-cols-2">
        <div>
          <dt className="text-xs text-muted-foreground">Username</dt>
          <dd dir="auto" className="text-sm font-medium">
            {user?.username}
          </dd>
        </div>
        <div>
          <dt className="text-xs text-muted-foreground">Role</dt>
          <dd className="text-sm font-medium capitalize">{user?.role}</dd>
        </div>
      </dl>
      <PasswordForm />
      <TwoFactorSection />
      <Button
        variant="outline"
        className="h-11 w-fit px-5"
        onClick={() => confirmLeave() && void logout()}
      >
        <LogOut aria-hidden="true" />
        Sign out
      </Button>
    </section>
  )
}
