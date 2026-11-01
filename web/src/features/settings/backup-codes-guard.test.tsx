// @vitest-environment jsdom
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { Link, RouterProvider, createMemoryRouter } from 'react-router-dom'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { ApiError, setAuthHandlers, setupTwoFactor, fetchTwoFactor } from '@/lib/api'
import { LEAVE_CODES_MESSAGE } from '@/lib/leave-guard'
import { TwoFactorSection } from './TwoFactorSection'
import { stubApi } from './test-utils'

afterEach(() => {
  cleanup()
  setAuthHandlers(null)
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

async function showCodes() {
  stubApi((c) => {
    if (c.url === '/api/auth/2fa/setup')
      return { json: { secret: 'JBSWY3DPEHPK3PXP', otpauth_uri: 'otpauth://totp/x?secret=A' } }
    if (c.url === '/api/auth/2fa/enable') return { json: { backup_codes: ['AAAAA-BBB22'] } }
    return { json: { enabled: false, backup_codes_remaining: 0 } }
  })
  const router = createMemoryRouter(
    [
      {
        path: '/',
        element: (
          <>
            <Link to="/other">go</Link>
            <TwoFactorSection />
          </>
        ),
      },
      { path: '/other', element: <p>other page</p> },
    ],
    { initialEntries: ['/'] },
  )
  render(
    <QueryClientProvider client={new QueryClient()}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  )
  fireEvent.click(await screen.findByRole('button', { name: 'Enable two-factor' }))
  fireEvent.change(screen.getByLabelText('Current password'), { target: { value: 'pw' } })
  fireEvent.click(screen.getByRole('button', { name: 'Continue' }))
  fireEvent.change(await screen.findByLabelText('Code from the app'), {
    target: { value: '123456' },
  })
  fireEvent.click(screen.getByRole('button', { name: 'Turn on' }))
  await screen.findByTestId('backup-codes')
  await act(() => new Promise((r) => setTimeout(r, 20))) // let the blocker register
  return router
}

describe('unsaved backup codes', () => {
  it('keeps the codes when the user declines leaving (link and history back)', async () => {
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(false)
    const router = await showCodes()
    fireEvent.click(screen.getByText('go'))
    await waitFor(() => expect(confirm).toHaveBeenCalledWith(LEAVE_CODES_MESSAGE))
    expect(router.state.location.pathname).toBe('/')
    expect(screen.getByTestId('backup-codes')).toBeTruthy()

    confirm.mockClear()
    await router.navigate('/other').catch(() => {})
    await waitFor(() => expect(confirm).toHaveBeenCalled())
    expect(router.state.location.pathname).toBe('/')
    expect(screen.getByTestId('backup-codes')).toBeTruthy()
  })

  it('navigates when the user confirms', async () => {
    vi.spyOn(window, 'confirm').mockReturnValue(true)
    const router = await showCodes()
    fireEvent.click(screen.getByText('go'))
    await waitFor(() => expect(router.state.location.pathname).toBe('/other'))
  })

  it('does not ask after the codes are acknowledged', async () => {
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(false)
    const router = await showCodes()
    fireEvent.click(screen.getByLabelText('I have saved these codes'))
    fireEvent.click(screen.getByText('go'))
    await waitFor(() => expect(router.state.location.pathname).toBe('/other'))
    expect(confirm).not.toHaveBeenCalled()
  })
})

describe('two-factor 401 handling', () => {
  it('status read: any 401 reaches the auth handler', async () => {
    const onUnauthorized = vi.fn()
    setAuthHandlers({ onUnauthorized, onPasswordChangeRequired: vi.fn() })
    stubApi(() => ({ status: 401, json: { error: 'authentication required' } }))
    await expect(fetchTwoFactor()).rejects.toBeInstanceOf(ApiError)
    expect(onUnauthorized).toHaveBeenCalledTimes(1)
  })

  it('management: "authentication required" is session lost, wrong credentials are not', async () => {
    const onUnauthorized = vi.fn()
    setAuthHandlers({ onUnauthorized, onPasswordChangeRequired: vi.fn() })
    stubApi(() => ({ status: 401, json: { error: 'invalid credentials' } }))
    await expect(setupTwoFactor('x')).rejects.toBeInstanceOf(ApiError)
    expect(onUnauthorized).not.toHaveBeenCalled()
    stubApi(() => ({ status: 401, json: { error: 'authentication required' } }))
    await expect(setupTwoFactor('x')).rejects.toBeInstanceOf(ApiError)
    expect(onUnauthorized).toHaveBeenCalledTimes(1)
  })
})
