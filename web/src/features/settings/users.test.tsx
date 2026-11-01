// @vitest-environment jsdom
import { cleanup, fireEvent, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import type { AdminUser } from '@/lib/types'
import { PasswordRevealDialog } from './PasswordRevealDialog'
import { renderWith, stubApi, type Call } from './test-utils'
import { UsersSection, userStatus } from './UsersSection'

const toastError = vi.hoisted(() => vi.fn())
const toastSuccess = vi.hoisted(() => vi.fn())
vi.mock('sonner', () => ({
  toast: { error: toastError, success: toastSuccess, warning: vi.fn() },
}))

const user = (over: Partial<AdminUser> = {}): AdminUser => ({
  id: 1,
  username: 'tomer',
  role: 'admin',
  disabled: false,
  must_change_password: false,
  created_at: '2026-10-01T10:00:00Z',
  last_login: '2026-10-31T10:00:00Z',
  ...over,
})
const dana = user({ id: 2, username: 'dana', role: 'viewer', last_login: null })

const SECRET = 'Tmp-Secret-4242-xyz'

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  localStorage.clear()
  sessionStorage.clear()
  toastError.mockReset()
  toastSuccess.mockReset()
})
beforeEach(() => {
  localStorage.clear()
})

describe('userStatus', () => {
  it('prefers Disabled over Must change password over Active', () => {
    expect(userStatus({ disabled: true, must_change_password: true })).toBe('Disabled')
    expect(userStatus({ disabled: false, must_change_password: true })).toBe('Must change password')
    expect(userStatus({ disabled: false, must_change_password: false })).toBe('Active')
  })
})

describe('PasswordRevealDialog', () => {
  it('Close stays disabled until "I have saved it" is ticked', () => {
    const onClose = vi.fn()
    renderWith(<PasswordRevealDialog username="dana" password={SECRET} onClose={onClose} />)
    expect(screen.getByTestId('one-time-password').textContent).toBe(SECRET)
    expect(screen.getByText(/will not be shown again/)).toBeTruthy()
    const buttons = screen.getAllByRole('button', { name: 'Close' }) as HTMLButtonElement[]
    // The X and the body button are both disabled while the box is unticked.
    expect(buttons.every((b) => b.disabled)).toBe(true)
    fireEvent.click(buttons[buttons.length - 1])
    fireEvent.keyDown(screen.getByTestId('dialog'), { key: 'Escape' })
    expect(onClose).not.toHaveBeenCalled()

    fireEvent.click(screen.getByRole('checkbox', { name: 'I have saved it' }))
    const enabled = screen.getAllByRole('button', { name: 'Close' }) as HTMLButtonElement[]
    expect(enabled.every((b) => !b.disabled)).toBe(true)
    fireEvent.click(enabled[enabled.length - 1])
    expect(onClose).toHaveBeenCalledTimes(1)
  })

  it('copies via the clipboard API', async () => {
    const writeText = vi.fn(() => Promise.resolve())
    vi.stubGlobal('navigator', { ...navigator, clipboard: { writeText } })
    renderWith(<PasswordRevealDialog username="dana" password={SECRET} onClose={() => {}} />)
    fireEvent.click(screen.getByRole('button', { name: 'Copy' }))
    await waitFor(() => expect(writeText).toHaveBeenCalledWith(SECRET))
    // The toast text must never contain the password.
    expect(JSON.stringify(toastSuccess.mock.calls)).not.toContain(SECRET)
  })

  it('persists nowhere: no storage, no URL, no console', () => {
    const spies = (['log', 'info', 'warn', 'error', 'debug'] as const).map((m) =>
      vi.spyOn(console, m).mockImplementation(() => {}),
    )
    renderWith(<PasswordRevealDialog username="dana" password={SECRET} onClose={() => {}} />)
    expect(JSON.stringify({ ...localStorage })).not.toContain(SECRET)
    expect(JSON.stringify({ ...sessionStorage })).not.toContain(SECRET)
    expect(location.href).not.toContain(SECRET)
    expect(document.cookie).not.toContain(SECRET)
    for (const s of spies) expect(JSON.stringify(s.mock.calls)).not.toContain(SECRET)
    spies.forEach((s) => s.mockRestore())
  })
})

describe('UsersSection', () => {
  const listAnd = (extra: (c: Call) => ReturnType<Parameters<typeof stubApi>[0]> | undefined) =>
    stubApi((c) => extra(c) ?? { json: [user(), dana] })

  it('renders status, role and relative times', async () => {
    listAnd(() => undefined)
    renderWith(<UsersSection />)
    const rows = await screen.findAllByTestId('user-row')
    expect(rows).toHaveLength(2)
    expect(within(rows[1]).getByText('Active')).toBeTruthy()
    expect(rows[1].textContent).toContain('Last login never')
    expect(rows[0].textContent).toContain('You')
  })

  it('your own role/disable/reset controls are inert and explain why', async () => {
    const calls = listAnd(() => undefined)
    renderWith(<UsersSection />)
    const rows = await screen.findAllByTestId('user-row')
    const self = within(rows[0])
    for (const name of ['Make viewer', 'Disable', 'Reset password']) {
      const b = self.getByRole('button', { name })
      expect(b.getAttribute('aria-disabled')).toBe('true')
      expect(b.getAttribute('title')).toBeTruthy()
      fireEvent.click(b)
    }
    expect(screen.queryByTestId('confirm-dialog')).toBeNull()
    expect(calls.filter((c) => c.method !== 'GET')).toEqual([])
  })

  it('other users can be promoted; the server message of a refused change is shown', async () => {
    const calls = listAnd((c) =>
      c.method === 'PATCH'
        ? {
            status: 409,
            json: { error: 'cannot change the last active admin', code: 'forbidden_change' },
          }
        : undefined,
    )
    renderWith(<UsersSection />)
    const rows = await screen.findAllByTestId('user-row')
    fireEvent.click(within(rows[1]).getByRole('button', { name: 'Make admin' }))
    await waitFor(() =>
      expect(toastError).toHaveBeenCalledWith('cannot change the last active admin'),
    )
    expect(calls.find((c) => c.method === 'PATCH')).toMatchObject({
      url: '/api/users/2',
      body: { role: 'admin' },
    })
  })

  it('Disable asks for confirmation first', async () => {
    const calls = listAnd((c) =>
      c.method === 'PATCH' ? { json: { ...dana, disabled: true } } : undefined,
    )
    renderWith(<UsersSection />)
    const rows = await screen.findAllByTestId('user-row')
    fireEvent.click(within(rows[1]).getByRole('button', { name: 'Disable' }))
    expect(calls.filter((c) => c.method === 'PATCH')).toHaveLength(0)
    fireEvent.click(
      within(screen.getByTestId('confirm-dialog')).getByRole('button', { name: 'Disable' }),
    )
    await waitFor(() => expect(calls.filter((c) => c.method === 'PATCH')).toHaveLength(1))
    expect(calls.find((c) => c.method === 'PATCH')?.body).toEqual({ disabled: true })
  })

  it('create with a generated password reveals it once and never caches it', async () => {
    listAnd((c) =>
      c.method === 'POST'
        ? {
            status: 201,
            json: {
              user: user({ id: 3, username: 'newbie', role: 'viewer' }),
              initial_password: SECRET,
            },
          }
        : undefined,
    )
    const { qc } = renderWith(<UsersSection />)
    await screen.findAllByTestId('user-row')
    fireEvent.click(screen.getByRole('button', { name: 'Add user' }))
    fireEvent.change(screen.getByLabelText('Username'), { target: { value: 'newbie' } })
    fireEvent.click(screen.getByRole('button', { name: 'Create user' }))
    const box = await screen.findByTestId('one-time-password')
    expect(box.textContent).toBe(SECRET)

    const dump = () =>
      JSON.stringify([
        qc
          .getQueryCache()
          .getAll()
          .map((q) => q.state.data),
        qc
          .getMutationCache()
          .getAll()
          .map((m) => [m.state.data, m.state.variables]),
      ])
    expect(dump()).not.toContain(SECRET)

    fireEvent.click(screen.getByRole('checkbox', { name: 'I have saved it' }))
    const closers = screen.getAllByRole('button', { name: 'Close' })
    fireEvent.click(closers[closers.length - 1])
    await waitFor(() => expect(screen.queryByTestId('one-time-password')).toBeNull())
    expect(document.body.textContent).not.toContain(SECRET)
    expect(dump()).not.toContain(SECRET)
  })

  it('validates the username and optional password before sending', async () => {
    const calls = listAnd(() => undefined)
    renderWith(<UsersSection />)
    await screen.findAllByTestId('user-row')
    fireEvent.click(screen.getByRole('button', { name: 'Add user' }))
    fireEvent.change(screen.getByLabelText('Username'), { target: { value: 'a b' } })
    fireEvent.click(screen.getByRole('button', { name: 'Create user' }))
    expect((await screen.findAllByRole('alert')).length).toBeGreaterThan(0)
    expect(calls.filter((c) => c.method === 'POST')).toHaveLength(0)
    fireEvent.change(screen.getByLabelText('Username'), { target: { value: 'valid.name' } })
    fireEvent.change(screen.getByLabelText('Password (optional)'), { target: { value: 'short' } })
    fireEvent.click(screen.getByRole('button', { name: 'Create user' }))
    expect(calls.filter((c) => c.method === 'POST')).toHaveLength(0)
  })

  it('shows user_exists inline', async () => {
    listAnd((c) =>
      c.method === 'POST'
        ? { status: 409, json: { error: 'username already exists', code: 'user_exists' } }
        : undefined,
    )
    renderWith(<UsersSection />)
    await screen.findAllByTestId('user-row')
    fireEvent.click(screen.getByRole('button', { name: 'Add user' }))
    fireEvent.change(screen.getByLabelText('Username'), { target: { value: 'dana' } })
    fireEvent.click(screen.getByRole('button', { name: 'Create user' }))
    await screen.findByText('username already exists')
  })

  it('reset password confirms, then reveals the new password once', async () => {
    const calls = listAnd((c) =>
      c.method === 'POST' ? { json: { initial_password: SECRET } } : undefined,
    )
    const { qc } = renderWith(<UsersSection />)
    const rows = await screen.findAllByTestId('user-row')
    fireEvent.click(within(rows[1]).getByRole('button', { name: 'Reset password' }))
    expect(calls.filter((c) => c.method === 'POST')).toHaveLength(0)
    fireEvent.click(
      within(screen.getByTestId('confirm-dialog')).getByRole('button', { name: 'Reset password' }),
    )
    expect((await screen.findByTestId('one-time-password')).textContent).toBe(SECRET)
    expect(calls.find((c) => c.method === 'POST')?.url).toBe('/api/users/2/reset-password')
    expect(
      JSON.stringify(
        qc
          .getMutationCache()
          .getAll()
          .map((m) => m.state.data),
      ),
    ).not.toContain(SECRET)
  })
})
