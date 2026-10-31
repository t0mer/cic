import { Eye, EyeOff } from 'lucide-react'
import { useEffect, useRef, useState, type FormEvent } from 'react'

import { Button } from '@/components/ui/button'
import { FieldError, Input, Label } from '@/components/ui/field'
import { ApiError } from '@/lib/api'
import { AuthShell } from './AuthShell'
import { useAuth } from './auth-context'

export function loginErrorMessage(err: unknown): string {
  if (err instanceof ApiError) {
    if (err.status === 401) return 'Invalid credentials'
    if (err.status === 429)
      return err.retryAfter !== null
        ? `Too many attempts. Try again in ${err.retryAfter}s.`
        : 'Too many attempts. Try again later.'
  }
  return 'Could not sign in. Try again.'
}

export function LoginPage() {
  const { login } = useAuth()
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [show, setShow] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const userRef = useRef<HTMLInputElement>(null)
  const passRef = useRef<HTMLInputElement>(null)

  useEffect(() => {
    userRef.current?.focus()
  }, [])

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (busy) return
    setBusy(true)
    setError(null)
    try {
      await login(username.trim(), password)
    } catch (err) {
      setError(loginErrorMessage(err))
      setBusy(false)
      passRef.current?.focus()
    }
  }

  return (
    <AuthShell title="Sign in">
      <form onSubmit={(e) => void submit(e)} className="flex flex-col gap-4" noValidate>
        <div className="flex flex-col gap-1.5">
          <Label htmlFor="login-username">Username</Label>
          <Input
            id="login-username"
            ref={userRef}
            name="username"
            autoComplete="username"
            autoCapitalize="none"
            spellCheck={false}
            required
            value={username}
            onChange={(e) => setUsername(e.target.value)}
            disabled={busy}
            aria-describedby={error ? 'login-error' : undefined}
          />
        </div>
        <div className="flex flex-col gap-1.5">
          <Label htmlFor="login-password">Password</Label>
          <div className="relative">
            <Input
              id="login-password"
              ref={passRef}
              name="password"
              type={show ? 'text' : 'password'}
              autoComplete="current-password"
              required
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              disabled={busy}
              className="pe-12"
              aria-invalid={error ? true : undefined}
              aria-describedby={error ? 'login-error' : undefined}
            />
            <button
              type="button"
              onClick={() => setShow((s) => !s)}
              aria-label={show ? 'Hide password' : 'Show password'}
              aria-pressed={show}
              className="absolute inset-y-0 end-0 inline-flex w-11 items-center justify-center rounded-md text-muted-foreground hover:text-foreground focus-visible:ring-[3px] focus-visible:ring-ring focus-visible:outline-none"
            >
              {show ? (
                <EyeOff className="size-5" aria-hidden="true" />
              ) : (
                <Eye className="size-5" aria-hidden="true" />
              )}
            </button>
          </div>
        </div>
        {error ? <FieldError id="login-error">{error}</FieldError> : null}
        <Button type="submit" className="h-11" disabled={busy || !username.trim() || !password}>
          {busy ? 'Signing in…' : 'Sign in'}
        </Button>
      </form>
    </AuthShell>
  )
}
