import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { ApiError, fetchDevices, login, sendCommand, setAuthHandlers, setCsrfToken } from './api'

const json = (status: number, body: unknown, headers: Record<string, string> = {}) =>
  new Response(body === undefined ? null : JSON.stringify(body), { status, headers })

let fetchMock: ReturnType<typeof vi.fn>
const onUnauthorized = vi.fn()
const onPasswordChangeRequired = vi.fn()

beforeEach(() => {
  fetchMock = vi.fn()
  vi.stubGlobal('fetch', fetchMock)
  setCsrfToken('tok-123')
  setAuthHandlers({ onUnauthorized, onPasswordChangeRequired })
})
afterEach(() => {
  vi.unstubAllGlobals()
  setAuthHandlers(null)
  setCsrfToken(null)
  onUnauthorized.mockClear()
  onPasswordChangeRequired.mockClear()
})

const headersOf = (call: number) =>
  (fetchMock.mock.calls[call][1] as RequestInit).headers as Record<string, string>

describe('api wrapper', () => {
  it('sends no CSRF header on GET', async () => {
    fetchMock.mockResolvedValue(json(200, []))
    await fetchDevices()
    expect(headersOf(0)['X-CSRF-Token']).toBeUndefined()
  })

  it('sends the CSRF header and JSON body on POST', async () => {
    fetchMock.mockResolvedValue(json(202, { id: '1' }))
    await sendCommand({ prefix: 'dradis', device: 'pixel' }, 'ping', {})
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toBe('/api/devices/dradis/pixel/ping')
    expect(init.method).toBe('POST')
    expect((init.headers as Record<string, string>)['X-CSRF-Token']).toBe('tok-123')
    expect(init.body).toBe('{}')
  })

  it('401 triggers the unauthorized handler', async () => {
    fetchMock.mockResolvedValue(json(401, { error: 'unauthorized' }))
    await expect(fetchDevices()).rejects.toMatchObject({ status: 401 })
    expect(onUnauthorized).toHaveBeenCalledTimes(1)
  })

  it('a failed login does not trigger it', async () => {
    fetchMock.mockResolvedValue(json(401, { error: 'invalid credentials' }))
    await expect(login('a', 'b')).rejects.toBeInstanceOf(ApiError)
    expect(onUnauthorized).not.toHaveBeenCalled()
  })

  it('403 password change required triggers its handler', async () => {
    fetchMock.mockResolvedValue(json(403, { error: 'password change required' }))
    await expect(fetchDevices()).rejects.toMatchObject({ status: 403 })
    expect(onPasswordChangeRequired).toHaveBeenCalledTimes(1)
    expect(onUnauthorized).not.toHaveBeenCalled()
  })

  it('parses code, message and Retry-After', async () => {
    fetchMock.mockResolvedValue(
      json(429, { error: 'slow down', code: 'rate_limited' }, { 'Retry-After': '7' }),
    )
    await expect(sendCommand({ prefix: 'a', device: 'b' }, 'sms', {})).rejects.toMatchObject({
      status: 429,
      code: 'rate_limited',
      retryAfter: 7,
      message: 'slow down',
    })
  })

  it('encodes path segments', async () => {
    fetchMock.mockResolvedValue(json(202, {}))
    await sendCommand({ prefix: 'a/b', device: 'c d' }, 'refresh-status')
    expect(fetchMock.mock.calls[0][0]).toBe('/api/devices/a%2Fb/c%20d/refresh-status')
    expect((fetchMock.mock.calls[0][1] as RequestInit).body).toBeUndefined()
  })
})

describe('command POST failure handling', () => {
  const dev = { prefix: 'dradis', device: 'pixel' }
  it('a network failure becomes outcome_unknown', async () => {
    fetchMock.mockRejectedValue(new TypeError('Failed to fetch'))
    await expect(sendCommand(dev, 'sms', {})).rejects.toMatchObject({ code: 'outcome_unknown' })
  })
  it('a 5xx without a code becomes outcome_unknown, with a code it is kept', async () => {
    fetchMock.mockResolvedValueOnce(json(502, { error: 'bad gateway' }))
    await expect(sendCommand(dev, 'sms', {})).rejects.toMatchObject({ code: 'outcome_unknown' })
    fetchMock.mockResolvedValueOnce(json(503, { error: 'x', code: 'not_connected' }))
    await expect(sendCommand(dev, 'sms', {})).rejects.toMatchObject({ code: 'not_connected' })
    fetchMock.mockResolvedValueOnce(json(400, { error: 'bad', code: 'invalid' }))
    await expect(sendCommand(dev, 'sms', {})).rejects.toMatchObject({ code: 'invalid' })
  })
  it('GETs keep the plain connection failure', async () => {
    fetchMock.mockRejectedValue(new TypeError('Failed to fetch'))
    await expect(fetchDevices()).rejects.toBeInstanceOf(TypeError)
  })
  it('a CSRF 403 triggers the refresh handler', async () => {
    const onCsrfRejected = vi.fn()
    setAuthHandlers({ onUnauthorized, onPasswordChangeRequired, onCsrfRejected })
    fetchMock.mockResolvedValue(json(403, { error: 'invalid csrf token' }))
    await expect(sendCommand(dev, 'ping', {})).rejects.toMatchObject({ status: 403 })
    expect(onCsrfRejected).toHaveBeenCalledTimes(1)
    fetchMock.mockResolvedValue(json(403, { error: 'cross-site request rejected' }))
    await expect(sendCommand(dev, 'ping', {})).rejects.toBeDefined()
    expect(onCsrfRejected).toHaveBeenCalledTimes(2)
  })
  it('change-password adopts the rotated CSRF token immediately', async () => {
    const { changePassword, getCsrfToken } = await import('./api')
    fetchMock.mockResolvedValue(
      json(200, {
        user: { username: 'u', role: 'admin', must_change_password: false },
        csrf_token: 'NEW',
      }),
    )
    await changePassword('old', 'new-long-password')
    expect(getCsrfToken()).toBe('NEW')
  })
})
