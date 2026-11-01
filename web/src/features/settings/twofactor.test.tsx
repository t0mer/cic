// @vitest-environment jsdom
import { cleanup, fireEvent, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { TwoFactorSection, twoFactorError } from './TwoFactorSection'
import { ApiError } from '@/lib/api'
import { renderWith, stubApi } from './test-utils'

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

describe('twoFactorError', () => {
  it('maps statuses to friendly text', () => {
    expect(twoFactorError(new ApiError(401, 'x'))).toBe('Incorrect password or code.')
    expect(twoFactorError(new ApiError(429, 'x', null, 5))).toContain('5s')
    expect(twoFactorError(new Error('boom'))).toBe('Something went wrong. Try again.')
  })
})

describe('TwoFactorSection', () => {
  it('shows Off with an Enable button', async () => {
    stubApi(() => ({ json: { enabled: false, backup_codes_remaining: 0 } }))
    renderWith(<TwoFactorSection />)
    expect(await screen.findByRole('button', { name: 'Enable two-factor' })).toBeTruthy()
  })

  it('warns when few backup codes remain and sends password + code to disable', async () => {
    const calls = stubApi((c) =>
      c.method === 'GET' ? { json: { enabled: true, backup_codes_remaining: 2 } } : { status: 204 },
    )
    renderWith(<TwoFactorSection />)
    expect((await screen.findByTestId('backup-remaining')).textContent).toContain('2 backup codes')
    expect(screen.getByTestId('backup-remaining').textContent).toContain('soon')
    fireEvent.click(screen.getByRole('button', { name: 'Turn off' }))
    fireEvent.change(screen.getByLabelText('Current password'), { target: { value: 'pw' } })
    fireEvent.change(screen.getByLabelText('Authentication code'), { target: { value: '123456' } })
    fireEvent.click(screen.getAllByRole('button', { name: 'Turn off' }).at(-1)!)
    await waitFor(() =>
      expect(calls.find((c) => c.url === '/api/auth/2fa/disable')?.body).toEqual({
        current_password: 'pw',
        code: '123456',
      }),
    )
  })
})
