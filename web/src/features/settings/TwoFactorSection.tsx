import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Copy, Download, ShieldCheck, TriangleAlert } from 'lucide-react'
import QRCode from 'qrcode'
import { useEffect, useRef, useState, type FormEvent } from 'react'
import { toast } from 'sonner'

import { Button } from '@/components/ui/button'
import { FieldError, Input, Label } from '@/components/ui/field'
import { ResponsiveDialog } from '@/components/ui/responsive-dialog'
import { CheckboxField } from '@/components/ui/switch-field'

import {
  ApiError,
  authKey,
  disableTwoFactor,
  enableTwoFactor,
  fetchTwoFactor,
  regenerateBackupCodes,
  setupTwoFactor,
  twoFactorKey,
} from '@/lib/api'
import type { TwoFactorSetup } from '@/lib/types'
import { copyText } from './PasswordRevealDialog'

/** Friendly text for any failure of a two-factor request. Never echoes secrets. */
export function twoFactorError(err: unknown): string {
  if (err instanceof ApiError) {
    if (err.status === 429)
      return err.retryAfter !== null
        ? `Too many attempts. Try again in ${err.retryAfter}s.`
        : 'Too many attempts. Try again later.'
    if (err.status === 401 || err.status === 403) return 'Incorrect password or code.'
    if (err.status === 400) return err.message
  }
  return 'Something went wrong. Try again.'
}

const LOW_CODES = 3

function BackupCodes({ codes, onClose }: { codes: string[]; onClose: () => void }) {
  const [saved, setSaved] = useState(false)
  const box = useRef<HTMLPreElement>(null)
  const text = codes.join('\n')

  // Closing the tab would lose codes that are shown only once.
  useEffect(() => {
    const guard = (e: BeforeUnloadEvent) => {
      e.preventDefault()
      e.returnValue = ''
    }
    window.addEventListener('beforeunload', guard)
    return () => window.removeEventListener('beforeunload', guard)
  }, [])

  const copy = async () => {
    if (await copyText(text, box.current)) toast.success('Backup codes copied')
    else toast.error('Could not copy. Select the codes and copy them manually.')
  }
  const download = () => {
    const url = URL.createObjectURL(
      new Blob([`CIC backup codes\n(each works once)\n\n${text}\n`], { type: 'text/plain' }),
    )
    const a = document.createElement('a')
    a.href = url
    a.download = 'cic-backup-codes.txt'
    a.click()
    URL.revokeObjectURL(url)
  }

  return (
    <>
      <div
        role="alert"
        className="flex gap-2 rounded-md border border-amber-500/60 bg-amber-50 p-3 text-sm text-amber-950 dark:bg-amber-950/40 dark:text-amber-100"
      >
        <TriangleAlert className="mt-0.5 size-4 shrink-0" aria-hidden="true" />
        <p>
          These codes will not be shown again. Each one works once if you lose your authenticator.
        </p>
      </div>
      <pre
        ref={box}
        data-testid="backup-codes"
        dir="ltr"
        className="grid grid-cols-2 gap-x-4 gap-y-1 rounded-md border bg-muted p-3 font-mono text-base select-all"
      >
        {codes.map((c) => (
          <span key={c}>{c}</span>
        ))}
      </pre>
      <div className="flex gap-2">
        <Button type="button" variant="outline" className="h-11 flex-1" onClick={() => void copy()}>
          <Copy aria-hidden="true" />
          Copy
        </Button>
        <Button type="button" variant="outline" className="h-11 flex-1" onClick={download}>
          <Download aria-hidden="true" />
          Download
        </Button>
      </div>
      <CheckboxField label="I have saved these codes" checked={saved} onChange={setSaved} />
      <Button type="button" className="h-11" disabled={!saved} onClick={onClose}>
        Close
      </Button>
    </>
  )
}

function CodeField({
  id,
  value,
  onChange,
  disabled,
  label = 'Authentication code',
  hint,
}: {
  id: string
  value: string
  onChange: (v: string) => void
  disabled: boolean
  label?: string
  hint?: string
}) {
  return (
    <div className="flex flex-col gap-1.5">
      <Label htmlFor={id}>{label}</Label>
      <Input
        id={id}
        autoComplete="off"
        spellCheck={false}
        autoCapitalize="characters"
        className="font-mono tracking-widest"
        value={value}
        onChange={(e) => onChange(e.target.value)}
        disabled={disabled}
        aria-describedby={hint ? `${id}-hint` : undefined}
      />
      {hint ? (
        <p id={`${id}-hint`} className="text-xs text-muted-foreground">
          {hint}
        </p>
      ) : null}
    </div>
  )
}

function EnableDialog({ onClose }: { onClose: () => void }) {
  const qc = useQueryClient()
  const [step, setStep] = useState<'password' | 'scan' | 'codes'>('password')
  const [password, setPassword] = useState('')
  const [code, setCode] = useState('')
  const [setup, setSetup] = useState<TwoFactorSetup | null>(null)
  const [qr, setQr] = useState<string | null>(null)
  const [codes, setCodes] = useState<string[]>([])
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const secretBox = useRef<HTMLElement>(null)

  // The QR is drawn in memory only: a data URL held in state, never sent anywhere.
  useEffect(() => {
    if (!setup) return
    let live = true
    QRCode.toDataURL(setup.otpauth_uri, { margin: 1, width: 220 })
      .then((u) => live && setQr(u))
      .catch(() => live && setQr(null))
    return () => {
      live = false
    }
  }, [setup])

  const run = async (fn: () => Promise<void>) => {
    if (busy) return
    setBusy(true)
    setError(null)
    try {
      await fn()
    } catch (err) {
      setError(twoFactorError(err))
    } finally {
      setBusy(false)
    }
  }

  const submitPassword = (e: FormEvent) => {
    e.preventDefault()
    void run(async () => {
      setSetup(await setupTwoFactor(password))
      setPassword('')
      setStep('scan')
    })
  }
  const submitCode = (e: FormEvent) => {
    e.preventDefault()
    void run(async () => {
      const res = await enableTwoFactor(code.trim())
      setCodes(res.backup_codes)
      setSetup(null)
      setQr(null)
      setCode('')
      setStep('codes')
      void qc.invalidateQueries({ queryKey: twoFactorKey })
      void qc.invalidateQueries({ queryKey: authKey })
    })
  }
  const copySecret = async () => {
    if (setup && (await copyText(setup.secret, secretBox.current))) toast.success('Key copied')
    else toast.error('Could not copy. Select the key and copy it manually.')
  }

  return (
    <ResponsiveDialog
      open
      onOpenChange={(o) => {
        if (!o) onClose()
      }}
      locked={busy || step === 'codes'}
      title={step === 'codes' ? 'Save your backup codes' : 'Enable two-factor authentication'}
      description={
        step === 'password'
          ? 'Confirm your password to start.'
          : step === 'scan'
            ? 'Scan the QR code with an authenticator app, then enter the code it shows.'
            : 'Two-factor authentication is on.'
      }
    >
      {step === 'password' ? (
        <form onSubmit={submitPassword} className="flex flex-col gap-4" noValidate>
          <div className="flex flex-col gap-1.5">
            <Label htmlFor="tf-password">Current password</Label>
            <Input
              id="tf-password"
              type="password"
              autoComplete="current-password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              disabled={busy}
            />
          </div>
          {error ? <FieldError id="tf-error">{error}</FieldError> : null}
          <Button type="submit" className="h-11" disabled={busy || !password}>
            {busy ? 'Checking…' : 'Continue'}
          </Button>
        </form>
      ) : null}
      {step === 'scan' && setup ? (
        <form onSubmit={submitCode} className="flex flex-col gap-4" noValidate>
          <div className="flex justify-center">
            {qr ? (
              <img
                src={qr}
                alt="QR code for your authenticator app"
                width={220}
                height={220}
                className="rounded-md border bg-white"
              />
            ) : (
              <div className="size-[220px] rounded-md border bg-muted" aria-hidden="true" />
            )}
          </div>
          <div className="flex flex-col gap-1.5">
            <span className="text-sm font-medium">Or enter this key manually</span>
            <div className="flex items-stretch gap-2">
              <code
                ref={secretBox}
                data-testid="totp-secret"
                dir="ltr"
                className="flex min-h-11 min-w-0 flex-1 items-center rounded-md border bg-muted px-3 font-mono text-sm break-all select-all"
              >
                {setup.secret}
              </code>
              <Button
                type="button"
                variant="outline"
                className="h-auto min-h-11 px-4"
                onClick={() => void copySecret()}
              >
                <Copy aria-hidden="true" />
                Copy
              </Button>
            </div>
          </div>
          <CodeField
            id="tf-code"
            value={code}
            onChange={setCode}
            disabled={busy}
            label="Code from the app"
          />
          {error ? <FieldError id="tf-error">{error}</FieldError> : null}
          <Button type="submit" className="h-11" disabled={busy || !code.trim()}>
            {busy ? 'Verifying…' : 'Turn on'}
          </Button>
        </form>
      ) : null}
      {step === 'codes' ? <BackupCodes codes={codes} onClose={onClose} /> : null}
    </ResponsiveDialog>
  )
}

/** Password + code, then either disables 2FA or issues a fresh set of backup codes. */
function ProofDialog({ mode, onClose }: { mode: 'disable' | 'regenerate'; onClose: () => void }) {
  const qc = useQueryClient()
  const [password, setPassword] = useState('')
  const [code, setCode] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [codes, setCodes] = useState<string[] | null>(null)
  const disable = mode === 'disable'

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (busy) return
    setBusy(true)
    setError(null)
    try {
      if (disable) {
        await disableTwoFactor(password, code.trim())
        toast.success('Two-factor authentication turned off')
      } else {
        setCodes((await regenerateBackupCodes(password, code.trim())).backup_codes)
      }
      setPassword('')
      setCode('')
      void qc.invalidateQueries({ queryKey: twoFactorKey })
      void qc.invalidateQueries({ queryKey: authKey })
      if (disable) onClose()
    } catch (err) {
      setError(twoFactorError(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <ResponsiveDialog
      open
      onOpenChange={(o) => {
        if (!o) onClose()
      }}
      locked={busy || codes !== null}
      title={
        codes
          ? 'Save your new backup codes'
          : disable
            ? 'Turn off two-factor authentication'
            : 'Generate new backup codes'
      }
      description={
        codes
          ? 'Your previous backup codes no longer work.'
          : 'Confirm with your password and a code from your app or a backup code.'
      }
    >
      {codes ? (
        <BackupCodes codes={codes} onClose={onClose} />
      ) : (
        <form onSubmit={(e) => void submit(e)} className="flex flex-col gap-4" noValidate>
          <div className="flex flex-col gap-1.5">
            <Label htmlFor="tf-proof-password">Current password</Label>
            <Input
              id="tf-proof-password"
              type="password"
              autoComplete="current-password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              disabled={busy}
            />
          </div>
          <CodeField
            id="tf-proof-code"
            value={code}
            onChange={setCode}
            disabled={busy}
            hint="A 6-digit code, or a backup code."
          />
          {error ? <FieldError id="tf-error">{error}</FieldError> : null}
          <Button
            type="submit"
            variant={disable ? 'destructive' : 'default'}
            className="h-11"
            disabled={busy || !password || !code.trim()}
          >
            {busy ? 'Working…' : disable ? 'Turn off' : 'Generate new codes'}
          </Button>
        </form>
      )}
    </ResponsiveDialog>
  )
}

export function TwoFactorSection() {
  const status = useQuery({ queryKey: twoFactorKey, queryFn: fetchTwoFactor, retry: false })
  const [dialog, setDialog] = useState<'enable' | 'disable' | 'regenerate' | null>(null)
  const close = () => setDialog(null)
  const enabled = status.data?.enabled === true
  const left = status.data?.backup_codes_remaining ?? 0

  return (
    <section
      aria-labelledby="acct-2fa-h"
      className="flex max-w-md flex-col gap-3 rounded-lg border p-4"
    >
      <h3 id="acct-2fa-h" className="flex items-center gap-2 text-sm font-medium">
        <ShieldCheck className="size-4" aria-hidden="true" />
        Two-factor authentication
      </h3>
      {status.isPending ? (
        <p className="text-sm text-muted-foreground">Loading…</p>
      ) : status.isError ? (
        <p role="alert" className="text-sm text-destructive">
          Could not load two-factor status.{' '}
          <button className="underline" onClick={() => void status.refetch()}>
            Retry
          </button>
        </p>
      ) : enabled ? (
        <>
          <p className="text-sm text-muted-foreground">
            On. You will be asked for a code from your authenticator app when you sign in.
          </p>
          <p
            data-testid="backup-remaining"
            className={
              left <= LOW_CODES
                ? 'text-sm font-medium text-amber-700 dark:text-amber-300'
                : 'text-sm text-muted-foreground'
            }
          >
            {left} backup {left === 1 ? 'code' : 'codes'} remaining
            {left <= LOW_CODES ? '. Generate new codes soon.' : ''}
          </p>
          <div className="flex flex-wrap gap-2">
            <Button variant="outline" className="h-11" onClick={() => setDialog('regenerate')}>
              Regenerate backup codes
            </Button>
            <Button variant="outline" className="h-11" onClick={() => setDialog('disable')}>
              Turn off
            </Button>
          </div>
        </>
      ) : (
        <>
          <p className="text-sm text-muted-foreground">
            Off. Add a second step to sign-in with an authenticator app.
          </p>
          <Button className="h-11 w-fit px-5" onClick={() => setDialog('enable')}>
            Enable two-factor
          </Button>
        </>
      )}
      {dialog === 'enable' ? <EnableDialog onClose={close} /> : null}
      {dialog === 'disable' || dialog === 'regenerate' ? (
        <ProofDialog mode={dialog} onClose={close} />
      ) : null}
    </section>
  )
}
