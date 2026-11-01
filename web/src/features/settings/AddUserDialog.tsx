import { Eye, EyeOff } from 'lucide-react'
import { useState, type FormEvent } from 'react'
import { toast } from 'sonner'

import { Button } from '@/components/ui/button'
import { FieldError, Input, Label } from '@/components/ui/field'
import { ResponsiveDialog } from '@/components/ui/responsive-dialog'
import { createUser } from '@/lib/api'
import type { CreatedUser, Role } from '@/lib/types'
import { MIN_PASSWORD, newUserPasswordProblem, usernameProblem } from '@/lib/validation'
import { useAction } from './queries'

type Create = ReturnType<typeof useAction<[Parameters<typeof createUser>[0]], CreatedUser>>

function Form({
  create,
  onCreated,
  onCancel,
}: {
  create: Create
  onCreated: (r: CreatedUser) => void
  onCancel: () => void
}) {
  const [username, setUsername] = useState('')
  const [role, setRole] = useState<Role>('viewer')
  const [password, setPassword] = useState('')
  const [show, setShow] = useState(false)
  const [touched, setTouched] = useState(false)

  const uProblem = usernameProblem(username)
  const pProblem = newUserPasswordProblem(password)

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setTouched(true)
    if (uProblem || pProblem || create.pending) return
    const res = await create.run({
      username,
      role,
      ...(password ? { password } : {}),
    })
    if (res) onCreated(res)
  }

  return (
    <form onSubmit={(e) => void submit(e)} className="flex flex-col gap-4" noValidate>
      <div className="flex flex-col gap-1.5">
        <Label htmlFor="nu-username">Username</Label>
        <Input
          id="nu-username"
          value={username}
          onChange={(e) => setUsername(e.target.value)}
          disabled={create.pending}
          autoComplete="off"
          data-1p-ignore
          data-lpignore="true"
          data-form-type="other"
          autoCapitalize="none"
          spellCheck={false}
          aria-invalid={touched && uProblem ? true : undefined}
          aria-describedby="nu-username-help"
        />
        <p
          id="nu-username-help"
          className={
            touched && uProblem ? 'text-sm text-destructive' : 'text-xs text-muted-foreground'
          }
        >
          3 to 64 letters, digits, dots, dashes or underscores.
        </p>
      </div>
      <div className="flex flex-col gap-1.5">
        <Label htmlFor="nu-role">Role</Label>
        <select
          id="nu-role"
          value={role}
          onChange={(e) => setRole(e.target.value === 'admin' ? 'admin' : 'viewer')}
          disabled={create.pending}
          className="h-11 w-full rounded-md border border-input bg-background px-3 text-base focus-visible:ring-[3px] focus-visible:ring-ring/50 focus-visible:outline-none"
        >
          <option value="viewer">Viewer (read only)</option>
          <option value="admin">Admin (full control)</option>
        </select>
      </div>
      <div className="flex flex-col gap-1.5">
        <Label htmlFor="nu-password">Password (optional)</Label>
        <div className="flex items-center gap-1">
          <Input
            id="nu-password"
            type={show ? 'text' : 'password'}
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            disabled={create.pending}
            autoComplete="new-password"
            data-1p-ignore
            data-lpignore="true"
            data-form-type="other"
            spellCheck={false}
            aria-invalid={touched && pProblem ? true : undefined}
            aria-describedby="nu-password-help"
          />
          <Button
            type="button"
            variant="ghost"
            className="size-11 shrink-0"
            aria-label={show ? 'Hide password' : 'Show password'}
            aria-pressed={show}
            onClick={() => setShow((s) => !s)}
          >
            {show ? <EyeOff aria-hidden="true" /> : <Eye aria-hidden="true" />}
          </Button>
        </div>
        <p
          id="nu-password-help"
          className={
            touched && pProblem ? 'text-sm text-destructive' : 'text-xs text-muted-foreground'
          }
        >
          At least {MIN_PASSWORD} characters. Leave empty and the server generates one, shown once.
        </p>
      </div>
      {touched && uProblem ? <FieldError id="nu-uerr">{uProblem}</FieldError> : null}
      {touched && pProblem ? <FieldError id="nu-perr">{pProblem}</FieldError> : null}
      {create.error ? <FieldError id="nu-error">{create.error}</FieldError> : null}
      <div className="flex flex-col-reverse gap-2 sm:flex-row sm:justify-end">
        <Button
          type="button"
          variant="outline"
          className="h-11 px-5"
          onClick={onCancel}
          disabled={create.pending}
        >
          Cancel
        </Button>
        <Button type="submit" className="h-11 px-5" disabled={create.pending}>
          {create.pending ? 'Creating…' : 'Create user'}
        </Button>
      </div>
    </form>
  )
}

export function AddUserDialog({
  open,
  onOpenChange,
  onCreated,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  /** Called with the server response; any one-time password goes to the caller's state only. */
  onCreated: (r: CreatedUser) => void
}) {
  // Owned here: the dialog is locked while the request runs, so the response is never lost.
  const create = useAction(createUser)
  const change = (o: boolean) => {
    if (!o) create.reset()
    onOpenChange(o)
  }
  return (
    <ResponsiveDialog
      open={open}
      onOpenChange={change}
      locked={create.pending}
      title="Add user"
      description="Create an account for someone who should use CIC."
    >
      <Form
        create={create}
        onCreated={(r) => {
          if (!r.initial_password) toast.success(`Created ${r.user.username}`)
          onCreated(r)
        }}
        onCancel={() => change(false)}
      />
    </ResponsiveDialog>
  )
}
