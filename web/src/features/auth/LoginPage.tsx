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

/** The server ends the pending login (expired token, too many wrong codes): back to the password. */
const isMfaExpired = (err: unknown) =>
  err instanceof ApiError &&
  err.status === 401 &&
  /expired|exhausted|too many|sign in again|log in again|start over/i.test(err.message)

export function mfaErrorMessage(err: unknown, backup: boolean): string {
  if (err instanceof ApiError) {
    if (err.status === 429)
      return err.retryAfter !== null
        ? `Too many attempts. Try again in ${err.retryAfter}s.`
        : 'Too many attempts. Try again later.'
    if (err.status === 401) return backup ? 'Invalid backup code' : 'Invalid code'
  }
  return 'Could not verify the code. Try again.'
}

function MfaStep({
  token,
  onBack,
  onExpired,
}: {
  token: string
  onBack: () => void
  onExpired: () => void
}) {
  const { verifyMfa } = useAuth()
  const [backup, setBackup] = useState(false)
  const [code, setCode] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const inputRef = useRef<HTMLInputElement>(null)

  useEffect(() => {
    inputRef.current?.focus()
  }, [backup])

  const submit = async (value: string) => {
    if (busy || !value) return
    setBusy(true)
    setError(null)
    try {
      await verifyMfa(token, value)
    } catch (err) {
      if (isMfaExpired(err)) {
        onExpired()
        return
      }
      setError(mfaErrorMessage(err, backup))
      setCode('')
      setBusy(false)
    }
  }

  // Focus returns once the input is enabled again after a failed attempt.
  useEffect(() => {
    if (!busy) inputRef.current?.focus()
  }, [busy])

  const onChange = (raw: string) => {
    if (backup) {
      setCode(raw)
      return
    }
    const digits = raw.replace(/\D/g, '').slice(0, 6)
    setCode(digits)
    if (digits.length === 6) void submit(digits)
  }

  return (
    <AuthShell title="Two-factor authentication">
      <form
        onSubmit={(e) => {
          e.preventDefault()
          void submit(code.trim())
        }}
        className="flex flex-col gap-4"
        noValidate
      >
        <div className="flex flex-col gap-1.5">
          <Label htmlFor="login-code">{backup ? 'Backup code' : 'Authentication code'}</Label>
          <Input
            id="login-code"
            ref={inputRef}
            name="code"
            key={backup ? 'backup' : 'totp'}
            {...(backup
              ? { type: 'text', autoComplete: 'off', autoCapitalize: 'characters' }
              : {
                  type: 'text',
                  inputMode: 'numeric',
                  autoComplete: 'one-time-code',
                  maxLength: 6,
                })}
            spellCheck={false}
            value={code}
            onChange={(e) => onChange(e.target.value)}
            disabled={busy}
            className="font-mono tracking-widest"
            aria-invalid={error ? true : undefined}
            aria-describedby={error ? 'login-error' : 'login-code-hint'}
          />
          <p id="login-code-hint" className="text-xs text-muted-foreground">
            {backup
              ? 'Enter one of your saved backup codes. Each works once.'
              : 'Enter the 6-digit code from your authenticator app.'}
          </p>
        </div>
        {error ? <FieldError id="login-error">{error}</FieldError> : null}
        {backup ? (
          <Button type="submit" className="h-11" disabled={busy || !code.trim()}>
            {busy ? 'Verifying…' : 'Verify'}
          </Button>
        ) : null}
        <Button
          type="button"
          variant="ghost"
          className="h-11"
          disabled={busy}
          onClick={() => {
            setBackup((b) => !b)
            setCode('')
            setError(null)
          }}
        >
          {backup ? 'Use authenticator code' : 'Use a backup code'}
        </Button>
        <Button type="button" variant="outline" className="h-11" disabled={busy} onClick={onBack}>
          Back
        </Button>
      </form>
    </AuthShell>
  )
}

export function LoginPage() {
  const { login } = useAuth()
  const [mfaToken, setMfaToken] = useState<string | null>(null)
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [show, setShow] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const userRef = useRef<HTMLInputElement>(null)
  const passRef = useRef<HTMLInputElement>(null)

  useEffect(() => {
    if (!mfaToken) userRef.current?.focus()
  }, [mfaToken])

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (busy) return
    setBusy(true)
    setError(null)
    try {
      const res = await login(username.trim(), password)
      if (res) {
        setMfaToken(res.mfaToken)
        setPassword('')
        setBusy(false)
      }
    } catch (err) {
      setError(loginErrorMessage(err))
      setBusy(false)
      passRef.current?.focus()
    }
  }

  if (mfaToken)
    return (
      <MfaStep
        token={mfaToken}
        onBack={() => setMfaToken(null)}
        onExpired={() => {
          setMfaToken(null)
          setError('Your sign-in expired. Enter your password again.')
        }}
      />
    )

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
