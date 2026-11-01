// @vitest-environment jsdom
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { useEffect, type ReactNode } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

const { warning } = vi.hoisted(() => ({ warning: vi.fn() }))
vi.mock('sonner', () => ({ toast: { warning, success: vi.fn(), error: vi.fn() } }))

import { clearResults, publishResult, useCommandResult } from '@/features/commands/results'
import { authKey, devicesKey, fetchDevices, getCsrfToken, setCsrfToken } from '@/lib/api'
import { AuthGate } from './AuthGate'
import { AuthProvider } from './AuthProvider'
import { useAuth } from './auth-context'

const res = (status: number, body?: unknown, headers: Record<string, string> = {}) =>
  new Response(body === undefined ? null : JSON.stringify(body), { status, headers })

const ME = { username: 'tomer', role: 'admin', must_change_password: false, csrf_token: 'csrf-A' }

let qc: QueryClient
let fetchMock: ReturnType<typeof vi.fn>
let routes: Record<string, () => Response>

function Probe() {
  const { user, logout } = useAuth()
  const r = useCommandResult('c1')
  return (
    <div>
      <span data-testid="who">{user?.username}</span>
      <span data-testid="res">{r?.status ?? 'none'}</span>
      <button onClick={() => void logout()}>logout</button>
    </div>
  )
}

const App = ({ children }: { children?: ReactNode }) => (
  <QueryClientProvider client={qc}>
    <AuthProvider>
      <AuthGate>{children ?? <Probe />}</AuthGate>
    </AuthProvider>
  </QueryClientProvider>
)

beforeEach(() => {
  qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  routes = {}
  fetchMock = vi.fn((url: string, init?: RequestInit) => {
    const key = `${init?.method ?? 'GET'} ${url}`
    const h = routes[key]
    if (!h) throw new Error(`unmocked ${key}`)
    return Promise.resolve(h())
  })
  vi.stubGlobal('fetch', fetchMock)
  setCsrfToken(null)
  clearResults()
})
afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  localStorage.clear()
  sessionStorage.clear()
})

function type(label: string, value: string) {
  fireEvent.change(screen.getByLabelText(label), { target: { value } })
}

describe('AuthProvider', () => {
  it('shows the login page on 401 from /me', async () => {
    routes['GET /api/auth/me'] = () => res(401, { error: 'unauthorized' })
    render(<App />)
    expect(await screen.findByRole('heading', { name: 'Sign in' })).toBeTruthy()
    expect(screen.getByLabelText('Username').getAttribute('autocomplete')).toBe('username')
    expect(screen.getByLabelText('Password').getAttribute('autocomplete')).toBe('current-password')
  })

  it('logs in, keeps the CSRF token in memory only, and renders the app', async () => {
    let loggedIn = false
    routes['GET /api/auth/me'] = () => (loggedIn ? res(200, ME) : res(401, { error: 'no' }))
    routes['POST /api/auth/login'] = () => {
      loggedIn = true
      return res(200, {
        user: { username: 'tomer', role: 'admin', must_change_password: false },
        csrf_token: 'csrf-A',
      })
    }
    render(<App />)
    await screen.findByRole('heading', { name: 'Sign in' })
    type('Username', 'tomer')
    type('Password', 'correct horse battery')
    fireEvent.click(screen.getByRole('button', { name: 'Sign in' }))
    await waitFor(() => expect(screen.getByTestId('who').textContent).toBe('tomer'))
    expect(getCsrfToken()).toBe('csrf-A')
    const dump =
      JSON.stringify({ ...localStorage }) + JSON.stringify({ ...sessionStorage }) + location.href
    expect(dump).not.toContain('csrf-A')
    expect(localStorage.length + sessionStorage.length).toBe(0)
  })

  it('shows a generic error for 401 and the Retry-After for 429', async () => {
    routes['GET /api/auth/me'] = () => res(401, { error: 'no' })
    routes['POST /api/auth/login'] = () => res(401, { error: 'invalid credentials' })
    render(<App />)
    await screen.findByRole('heading', { name: 'Sign in' })
    type('Username', 'nobody')
    type('Password', 'whatever')
    fireEvent.click(screen.getByRole('button', { name: 'Sign in' }))
    expect((await screen.findByRole('alert')).textContent).toBe('Invalid credentials')

    routes['POST /api/auth/login'] = () => res(429, { error: 'slow' }, { 'Retry-After': '42' })
    fireEvent.click(screen.getByRole('button', { name: 'Sign in' }))
    await waitFor(() => expect(screen.getByRole('alert').textContent).toContain('42s'))
  })

  it('disables the form while submitting', async () => {
    routes['GET /api/auth/me'] = () => res(401, { error: 'no' })
    let release: (r: Response) => void = () => {}
    fetchMock.mockImplementation((url: string, init?: RequestInit) => {
      const key = `${init?.method ?? 'GET'} ${url}`
      if (key === 'POST /api/auth/login') return new Promise<Response>((r) => (release = r))
      return Promise.resolve(routes[key]())
    })
    render(<App />)
    await screen.findByRole('heading', { name: 'Sign in' })
    type('Username', 'a')
    type('Password', 'b')
    fireEvent.click(screen.getByRole('button', { name: 'Sign in' }))
    await waitFor(() => expect(screen.getByRole('button', { name: 'Signing in…' })).toBeTruthy())
    expect((screen.getByLabelText('Username') as HTMLInputElement).disabled).toBe(true)
    release(res(401, { error: 'x' }))
    await screen.findByRole('alert')
  })

  it('forces a password change when /me says so', async () => {
    routes['GET /api/auth/me'] = () => res(200, { ...ME, must_change_password: true })
    render(<App />)
    expect(await screen.findByRole('heading', { name: 'Choose a new password' })).toBeTruthy()
    expect(screen.queryByTestId('who')).toBeNull()
    // client-side UX checks
    type('Current password', 'old-password')
    type('New password', 'short')
    type('Confirm new password', 'short')
    fireEvent.click(screen.getByRole('button', { name: 'Change password' }))
    expect((await screen.findByRole('alert')).textContent).toMatch(/at least 12/)
    expect(fetchMock.mock.calls.some((c) => String(c[0]).includes('change-password'))).toBe(false)
  })

  it('forces a password change after a 403 "password change required"', async () => {
    let must = false
    routes['GET /api/auth/me'] = () => res(200, { ...ME, must_change_password: must })
    routes['GET /api/devices'] = () => {
      must = true
      return res(403, { error: 'password change required' })
    }
    function Loader() {
      useEffect(() => {
        void fetchDevices().catch(() => {})
      }, [])
      return <span>ok</span>
    }
    render(
      <App>
        <Loader />
      </App>,
    )
    expect(await screen.findByRole('heading', { name: 'Choose a new password' })).toBeTruthy()
  })

  it('changes the password then proceeds', async () => {
    let must = true
    routes['GET /api/auth/me'] = () => res(200, { ...ME, must_change_password: must })
    routes['POST /api/auth/change-password'] = () => {
      must = false
      return res(204)
    }
    render(<App />)
    await screen.findByRole('heading', { name: 'Choose a new password' })
    type('Current password', 'old-password')
    type('New password', 'a-much-longer-password')
    type('Confirm new password', 'a-much-longer-password')
    fireEvent.click(screen.getByRole('button', { name: 'Change password' }))
    await waitFor(() => expect(screen.getByTestId('who').textContent).toBe('tomer'))
    const call = fetchMock.mock.calls.find((c) => String(c[0]).includes('change-password'))
    expect(JSON.parse((call?.[1] as RequestInit).body as string)).toEqual({
      current_password: 'old-password',
      new_password: 'a-much-longer-password',
    })
  })

  it('logout clears every cache, the CSRF token and command results', async () => {
    routes['GET /api/auth/me'] = () => res(200, ME)
    routes['POST /api/auth/logout'] = () => res(204)
    render(<App />)
    await waitFor(() => expect(screen.getByTestId('who').textContent).toBe('tomer'))
    qc.setQueryData(devicesKey, [{ key: 'x' }])
    publishResult({ id: 'c1', action: 'sms', device: 'a/b', status: 'confirmed_ok' })
    await waitFor(() => expect(screen.getByTestId('res').textContent).toBe('confirmed_ok'))
    fireEvent.click(screen.getByText('logout'))
    await screen.findByRole('heading', { name: 'Sign in' })
    expect(qc.getQueryData(devicesKey)).toBeUndefined()
    expect(qc.getQueryData(authKey)).toBeNull()
    expect(getCsrfToken()).toBeNull()
    const logoutCall = fetchMock.mock.calls.find((c) => String(c[0]).includes('/logout'))
    expect((logoutCall?.[1] as RequestInit).headers).toMatchObject({ 'X-CSRF-Token': 'csrf-A' })
  })

  it('returns to login when any call gets a 401', async () => {
    routes['GET /api/auth/me'] = () => res(200, ME)
    routes['GET /api/devices'] = () => res(401, { error: 'unauthorized' })
    function Loader() {
      useEffect(() => {
        void fetchDevices().catch(() => {})
      }, [])
      return <span>ok</span>
    }
    render(
      <App>
        <Loader />
      </App>,
    )
    expect(await screen.findByRole('heading', { name: 'Sign in' })).toBeTruthy()
    expect(getCsrfToken()).toBeNull()
  })
})

describe('session races', () => {
  it('a /me still in flight at logout cannot restore the CSRF token or the session', async () => {
    let meCalls = 0
    let releaseMe: (r: Response) => void = () => {}
    routes['POST /api/auth/logout'] = () => res(204)
    fetchMock.mockImplementation((url: string, init?: RequestInit) => {
      const key = `${init?.method ?? 'GET'} ${url}`
      if (key === 'GET /api/auth/me') {
        meCalls++
        if (meCalls === 1) return Promise.resolve(res(200, ME))
        return new Promise<Response>((r) => (releaseMe = r))
      }
      return Promise.resolve(routes[key]())
    })
    render(<App />)
    await waitFor(() => expect(screen.getByTestId('who').textContent).toBe('tomer'))
    void qc.refetchQueries({ queryKey: authKey }) // deferred second /me
    await waitFor(() => expect(meCalls).toBe(2))
    fireEvent.click(screen.getByText('logout'))
    await screen.findByRole('heading', { name: 'Sign in' })
    releaseMe(res(200, { ...ME, csrf_token: 'csrf-STALE' }))
    await new Promise((r) => setTimeout(r, 20))
    expect(getCsrfToken()).toBeNull()
    expect(qc.getQueryData(authKey)).toBeNull()
    expect(screen.queryByTestId('who')).toBeNull()
  })

  it('warns when the logout request fails but still returns to login', async () => {
    routes['GET /api/auth/me'] = () => res(200, ME)
    routes['POST /api/auth/logout'] = () => res(500, { error: 'boom' })
    render(<App />)
    await waitFor(() => expect(screen.getByTestId('who').textContent).toBe('tomer'))
    fireEvent.click(screen.getByText('logout'))
    await screen.findByRole('heading', { name: 'Sign in' })
    expect(warning).toHaveBeenCalledWith(expect.stringContaining('Could not sign out'))
  })

  it('a CSRF 403 refetches /me for a fresh token', async () => {
    let tok = 'csrf-A'
    routes['GET /api/auth/me'] = () => res(200, { ...ME, csrf_token: tok })
    routes['GET /api/devices'] = () => {
      tok = 'csrf-B'
      return res(403, { error: 'invalid csrf token' })
    }
    function Loader() {
      useEffect(() => {
        void fetchDevices().catch(() => {})
      }, [])
      return <span>ok</span>
    }
    render(
      <App>
        <Loader />
      </App>,
    )
    await waitFor(() => expect(getCsrfToken()).toBe('csrf-B'))
  })
})

describe('AuthProvider two-factor login', () => {
  const setup = () => {
    let loggedIn = false
    routes['GET /api/auth/me'] = () => (loggedIn ? res(200, ME) : res(401, { error: 'no' }))
    routes['POST /api/auth/login'] = () => res(200, { mfa_required: true, mfa_token: 'tok' })
    routes['POST /api/auth/login/2fa'] = () => {
      loggedIn = true
      return res(200, {
        user: { username: 'tomer', role: 'admin', must_change_password: false },
        csrf_token: 'csrf-A',
      })
    }
  }
  const toStep2 = async () => {
    render(<App />)
    await screen.findByRole('heading', { name: 'Sign in' })
    type('Username', 'tomer')
    type('Password', 'pw')
    fireEvent.click(screen.getByRole('button', { name: 'Sign in' }))
    await screen.findByRole('heading', { name: 'Two-factor authentication' })
  }

  it('auto-submits at 6 digits and signs in; token is not signed in before', async () => {
    setup()
    await toStep2()
    const input = screen.getByLabelText('Authentication code')
    expect(input.getAttribute('autocomplete')).toBe('one-time-code')
    expect(getCsrfToken()).toBeNull()
    fireEvent.change(input, { target: { value: '12345' } })
    expect(fetchMock.mock.calls.some(([u]) => String(u).endsWith('/login/2fa'))).toBe(false)
    fireEvent.change(input, { target: { value: '123456' } })
    await waitFor(() => expect(screen.getByTestId('who').textContent).toBe('tomer'))
    const call = fetchMock.mock.calls.find(([u]) => String(u).endsWith('/login/2fa'))!
    expect(JSON.parse(call[1].body)).toEqual({ mfa_token: 'tok', code: '123456' })
    expect(getCsrfToken()).toBe('csrf-A')
  })

  it('shows errors, handles 429 and the backup toggle', async () => {
    setup()
    await toStep2()
    routes['POST /api/auth/login/2fa'] = () => res(401, { error: 'invalid code' })
    fireEvent.change(screen.getByLabelText('Authentication code'), { target: { value: '000000' } })
    expect((await screen.findByRole('alert')).textContent).toBe('Invalid code')
    routes['POST /api/auth/login/2fa'] = () => res(429, { error: 'slow' }, { 'Retry-After': '9' })
    fireEvent.change(screen.getByLabelText('Authentication code'), { target: { value: '000000' } })
    await waitFor(() => expect(screen.getByRole('alert').textContent).toContain('9s'))
    fireEvent.click(screen.getByRole('button', { name: 'Use a backup code' }))
    expect(screen.getByLabelText('Backup code')).toBeTruthy()
  })

  it('returns to the password step when the pending login expired, and on Back', async () => {
    setup()
    await toStep2()
    routes['POST /api/auth/login/2fa'] = () => res(401, { error: 'sign-in expired' })
    fireEvent.change(screen.getByLabelText('Authentication code'), { target: { value: '000000' } })
    expect(await screen.findByRole('heading', { name: 'Sign in' })).toBeTruthy()
    expect(screen.getByRole('alert').textContent).toContain('expired')

    fireEvent.change(screen.getByLabelText('Password'), { target: { value: 'pw' } })
    fireEvent.click(screen.getByRole('button', { name: 'Sign in' }))
    await screen.findByRole('heading', { name: 'Two-factor authentication' })
    fireEvent.click(screen.getByRole('button', { name: 'Back' }))
    expect(await screen.findByRole('heading', { name: 'Sign in' })).toBeTruthy()
  })
})
