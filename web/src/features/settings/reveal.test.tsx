// @vitest-environment jsdom
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import type { ReactNode } from 'react'
import { Link, MemoryRouter, Outlet, Route, Routes } from 'react-router-dom'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { AuthContext } from '@/features/auth/auth-context'
import type { Role } from '@/lib/types'
import { RevealProvider } from './reveal'
import { SettingsPage } from './SettingsPage'
import { auth, stubApi } from './test-utils'

vi.mock('sonner', () => ({ toast: { error: vi.fn(), success: vi.fn(), warning: vi.fn() } }))

const SECRET = 'Tmp-Secret-4242-xyz'
const users = [
  {
    id: 1,
    username: 'Tomer',
    role: 'admin',
    disabled: false,
    must_change_password: false,
    created_at: '2026-10-01T10:00:00Z',
    last_login: null,
  },
  {
    id: 2,
    username: 'dana',
    role: 'viewer',
    disabled: false,
    must_change_password: false,
    created_at: '2026-10-01T10:00:00Z',
    last_login: null,
  },
]

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
  localStorage.clear()
  sessionStorage.clear()
})

function Shell() {
  return (
    <RevealProvider>
      <Link to="/elsewhere">go elsewhere</Link>
      <Outlet />
    </RevealProvider>
  )
}

function App({ qc, role }: { qc: QueryClient; role: Role | null }) {
  const tree: ReactNode = (
    <QueryClientProvider client={qc}>
      <AuthContext.Provider value={auth(role, 'tomer')}>
        <MemoryRouter initialEntries={['/settings?tab=users']}>
          <Routes>
            <Route element={<Shell />}>
              <Route path="/settings" element={<SettingsPage />} />
              <Route path="/elsewhere" element={<p>elsewhere page</p>} />
            </Route>
          </Routes>
        </MemoryRouter>
      </AuthContext.Provider>
    </QueryClientProvider>
  )
  return tree
}

const resetDana = async () => {
  const rows = await screen.findAllByTestId('user-row')
  fireEvent.click(within(rows[1]).getByRole('button', { name: 'Reset password' }))
  fireEvent.click(
    within(screen.getByTestId('confirm-dialog')).getByRole('button', { name: 'Reset password' }),
  )
}

function deferredApi() {
  let release: () => void = () => {}
  const gate = new Promise<void>((r) => (release = r))
  stubApi(async (c) => {
    if (c.method === 'POST') {
      await gate
      return { json: { initial_password: SECRET } }
    }
    return { json: users }
  })
  return () => release()
}

describe('RevealProvider', () => {
  it('survives tab changes and route changes, and leaks nowhere', async () => {
    const spies = (['log', 'info', 'warn', 'error', 'debug'] as const).map((m) =>
      vi.spyOn(console, m).mockImplementation(() => {}),
    )
    stubApi((c) => (c.method === 'POST' ? { json: { initial_password: SECRET } } : { json: users }))
    const qc = new QueryClient()
    render(<App qc={qc} role="admin" />)
    await resetDana()
    expect((await screen.findByTestId('one-time-password')).textContent).toBe(SECRET)
    expect(screen.getByText('If you lose this, reset the password again.')).toBeTruthy()
    expect(screen.getByText(/clipboard may keep a copy/)).toBeTruthy()

    fireEvent.mouseDown(screen.getByRole('tab', { name: 'Devices', hidden: true }))
    fireEvent.click(screen.getByRole('tab', { name: 'Devices', hidden: true }))
    await waitFor(() => expect(screen.queryAllByTestId('user-row')).toHaveLength(0))
    expect(screen.getByTestId('one-time-password').textContent).toBe(SECRET)
    fireEvent.click(screen.getByText('go elsewhere', { selector: 'a' }))
    expect(screen.getByText('elsewhere page', { exact: false })).toBeTruthy()
    expect(screen.getByTestId('one-time-password').textContent).toBe(SECRET)

    const dump = JSON.stringify([
      qc
        .getQueryCache()
        .getAll()
        .map((q) => q.state.data),
      qc
        .getMutationCache()
        .getAll()
        .map((m) => [m.state.data, m.state.variables]),
      { ...localStorage },
      { ...sessionStorage },
      location.href,
      spies.map((s) => s.mock.calls),
    ])
    expect(dump).not.toContain(SECRET)
  })

  it('a response that arrives after the section unmounted still shows the password', async () => {
    const release = deferredApi()
    render(<App qc={new QueryClient()} role="admin" />)
    await resetDana()
    fireEvent.click(screen.getByText('go elsewhere', { selector: 'a' }))
    await screen.findByText('elsewhere page')
    expect(screen.queryByTestId('one-time-password')).toBeNull()
    await act(async () => release())
    expect((await screen.findByTestId('one-time-password')).textContent).toBe(SECRET)
  })

  it('installs a beforeunload guard while open and removes it on close', async () => {
    stubApi((c) => (c.method === 'POST' ? { json: { initial_password: SECRET } } : { json: users }))
    render(<App qc={new QueryClient()} role="admin" />)
    const fire = () => {
      const e = new Event('beforeunload', { cancelable: true })
      window.dispatchEvent(e)
      return e.defaultPrevented
    }
    expect(fire()).toBe(false)
    await resetDana()
    await screen.findByTestId('one-time-password')
    expect(fire()).toBe(true)
    fireEvent.click(screen.getByRole('checkbox', { name: 'I have saved it' }))
    const closers = screen.getAllByRole('button', { name: 'Close' })
    fireEvent.click(closers[closers.length - 1])
    await waitFor(() => expect(screen.queryByTestId('one-time-password')).toBeNull())
    expect(fire()).toBe(false)
  })

  it('logout (or a 401) clears it', async () => {
    stubApi((c) => (c.method === 'POST' ? { json: { initial_password: SECRET } } : { json: users }))
    const qc = new QueryClient()
    const view = render(<App qc={qc} role="admin" />)
    await resetDana()
    await screen.findByTestId('one-time-password')
    view.rerender(<App qc={qc} role={null} />)
    expect(screen.queryByTestId('one-time-password')).toBeNull()
    expect(document.body.textContent).not.toContain(SECRET)
    // Signing back in as the same user must not bring it back.
    view.rerender(<App qc={qc} role="admin" />)
    expect(screen.queryByTestId('one-time-password')).toBeNull()
  })

  it('treats usernames case-insensitively when marking you', async () => {
    stubApi(() => ({ json: users }))
    render(<App qc={new QueryClient()} role="admin" />)
    const rows = await screen.findAllByTestId('user-row')
    expect(rows[0].textContent).toContain('You')
    expect(rows[1].textContent).not.toContain('You')
  })
})
