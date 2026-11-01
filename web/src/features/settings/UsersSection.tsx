import { Plus } from 'lucide-react'
import { useState } from 'react'
import { toast } from 'sonner'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { ConfirmDialog } from '@/components/ui/confirm-dialog'
import { useAuth } from '@/features/auth/auth-context'
import { resetUserPassword } from '@/lib/api'
import { useNow } from '@/lib/now'
import { relativeTime } from '@/lib/time'
import type { AdminUser } from '@/lib/types'
import { AddUserDialog } from './AddUserDialog'
import { useReveal } from './reveal'
import { useAction, useOnce, useUpdateUser, useUsers } from './queries'
import { QueryState } from './QueryState'

const rowBtn = 'h-11 px-3 aria-disabled:opacity-50 aria-disabled:cursor-not-allowed'

const ts = (iso: string | null) => {
  if (!iso) return null
  const ms = Date.parse(iso)
  return Number.isFinite(ms) ? Math.floor(ms / 1000) : null
}

export function userStatus(u: Pick<AdminUser, 'disabled' | 'must_change_password'>) {
  if (u.disabled) return 'Disabled'
  if (u.must_change_password) return 'Must change password'
  return 'Active'
}

const SELF_NOTE = 'You cannot change your own account here'

function UserRow({
  user,
  self,
  busy,
  now,
  onRole,
  onDisable,
  onEnable,
  onReset,
}: {
  user: AdminUser
  self: boolean
  busy: boolean
  now: number
  onRole: () => void
  onDisable: () => void
  onEnable: () => void
  onReset: () => void
}) {
  const status = userStatus(user)
  const guard = (fn: () => void) => () => {
    if (!busy && !self) fn()
  }
  const selfProps = self
    ? { 'aria-disabled': true as const, title: SELF_NOTE }
    : { 'aria-disabled': busy || undefined }
  const lastLogin = ts(user.last_login)
  return (
    <li
      className="flex flex-col gap-3 rounded-lg border p-3 md:flex-row md:items-center"
      data-testid="user-row"
    >
      <div className="min-w-0 flex-1">
        <div className="flex flex-wrap items-center gap-2">
          <span dir="auto" className="truncate font-medium">
            {user.username}
          </span>
          {self ? <Badge variant="outline">You</Badge> : null}
          <Badge variant={user.role === 'admin' ? 'default' : 'secondary'} className="capitalize">
            {user.role}
          </Badge>
          <Badge variant={user.disabled ? 'destructive' : 'outline'}>{status}</Badge>
        </div>
        <p className="mt-1 text-xs text-muted-foreground">
          Created {relativeTime(ts(user.created_at), now)} · Last login{' '}
          {lastLogin === null ? 'never' : relativeTime(lastLogin, now)}
        </p>
      </div>
      <div className="flex flex-wrap gap-1">
        <Button variant="outline" className={rowBtn} {...selfProps} onClick={guard(onRole)}>
          {user.role === 'admin' ? 'Make viewer' : 'Make admin'}
        </Button>
        {user.disabled ? (
          <Button
            variant="outline"
            className={rowBtn}
            aria-disabled={busy || undefined}
            onClick={() => !busy && onEnable()}
          >
            Enable
          </Button>
        ) : (
          <Button variant="outline" className={rowBtn} {...selfProps} onClick={guard(onDisable)}>
            Disable
          </Button>
        )}
        <Button
          variant="outline"
          className={rowBtn}
          {...(self
            ? {
                'aria-disabled': true as const,
                title: 'Change your own password from the Account tab',
              }
            : { 'aria-disabled': busy || undefined })}
          onClick={guard(onReset)}
        >
          Reset password
        </Button>
      </div>
    </li>
  )
}

export function UsersSection() {
  const { user: me } = useAuth()
  const users = useUsers()
  const update = useUpdateUser()
  const now = useNow()
  const [adding, setAdding] = useState(false)
  const [disabling, setDisabling] = useState<AdminUser | null>(null)
  const [resetting, setResetting] = useState<AdminUser | null>(null)
  // The one-time secret goes to the provider above the router: it outlives this section.
  const { show } = useReveal()
  const reset = useAction(resetUserPassword, () => void users.refetch())
  const once = useOnce()
  const patch = (id: AdminUser['id'], p: { role?: string; disabled?: boolean }) =>
    once((done) => update.mutate({ id, patch: p }, { onSettled: done }))

  const busyId = update.isPending ? update.variables.id : null

  const doReset = async () => {
    if (!resetting) return
    const target = resetting
    const res = await reset.run(target.id)
    if (res) {
      setResetting(null)
      void users.refetch()
      show(target.username, res.initial_password)
    }
  }

  return (
    <section aria-labelledby="users-h" className="flex flex-col gap-4">
      <div className="flex items-center justify-between gap-2">
        <h2 id="users-h" className="text-lg font-semibold">
          Users
        </h2>
        <Button className="h-11 px-4" onClick={() => setAdding(true)}>
          <Plus aria-hidden="true" />
          Add user
        </Button>
      </div>
      <QueryState query={users} what="users" empty="No users yet">
        {(list) => (
          <ul className="flex flex-col gap-2">
            {list.map((u) => (
              <UserRow
                key={u.id}
                user={u}
                self={u.username.toLowerCase() === me?.username.toLowerCase()}
                busy={busyId === u.id}
                now={now}
                onRole={() => patch(u.id, { role: u.role === 'admin' ? 'viewer' : 'admin' })}
                onDisable={() => setDisabling(u)}
                onEnable={() => patch(u.id, { disabled: false })}
                onReset={() => setResetting(u)}
              />
            ))}
          </ul>
        )}
      </QueryState>

      <AddUserDialog
        open={adding}
        onOpenChange={setAdding}
        onCreated={(r) => {
          setAdding(false)
          void users.refetch()
          if (r.initial_password) show(r.user.username, r.initial_password)
        }}
      />

      <ConfirmDialog
        open={disabling !== null}
        onOpenChange={(o) => !o && setDisabling(null)}
        title={`Disable ${disabling?.username ?? ''}?`}
        description="They are signed out and cannot sign in until you enable the account again."
        confirmLabel="Disable"
        pendingLabel="Disabling…"
        destructive
        pending={update.isPending}
        onConfirm={() => {
          if (!disabling) return
          const target = disabling
          once((done) =>
            update.mutate(
              { id: target.id, patch: { disabled: true } },
              {
                onSuccess: () => {
                  toast.success(`Disabled ${target.username}`)
                  setDisabling(null)
                },
                onError: () => setDisabling(null),
                onSettled: done,
              },
            ),
          )
        }}
      />

      <ConfirmDialog
        open={resetting !== null}
        onOpenChange={(o) => {
          if (!o) {
            setResetting(null)
            reset.reset()
          }
        }}
        title={`Reset password for ${resetting?.username ?? ''}?`}
        description="A new temporary password is shown once. Their sessions end and they must choose a new password at the next sign-in."
        confirmLabel="Reset password"
        pendingLabel="Resetting…"
        destructive
        pending={reset.pending}
        error={reset.error}
        onConfirm={() => void doReset()}
      />
    </section>
  )
}
