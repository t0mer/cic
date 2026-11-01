import { afterEach, describe, expect, it, vi } from 'vitest'

import { ApiError, createUser, fetchAllDevices, patchDevice, patchUser } from './api'
import { commandErrorMessage } from './errors'

const reply = (status: number, body: unknown, headers: Record<string, string> = {}) =>
  vi.stubGlobal(
    'fetch',
    vi.fn(() =>
      Promise.resolve(
        new Response(JSON.stringify(body), {
          status,
          headers: { 'content-type': 'application/json', ...headers },
        }),
      ),
    ),
  )

afterEach(() => vi.unstubAllGlobals())

async function fail(p: Promise<unknown>) {
  try {
    await p
  } catch (e) {
    return e as ApiError
  }
  throw new Error('expected a rejection')
}

describe('admin error mapping', () => {
  it('forbidden_change keeps the server message', async () => {
    reply(409, { error: 'cannot demote the last admin', code: 'forbidden_change' })
    const e = await fail(patchUser(1, { role: 'viewer' }))
    expect(e.code).toBe('forbidden_change')
    expect(commandErrorMessage(e)).toBe('cannot demote the last admin')
  })
  it('user_exists keeps the server message', async () => {
    reply(409, { error: 'username already taken', code: 'user_exists' })
    const e = await fail(createUser({ username: 'dana', role: 'viewer' }))
    expect(e.code).toBe('user_exists')
    expect(commandErrorMessage(e)).toBe('username already taken')
  })
  it('rate_limited uses Retry-After', async () => {
    reply(429, { error: 'slow down', code: 'rate_limited' }, { 'Retry-After': '17' })
    const e = await fail(patchUser(1, { disabled: true }))
    expect(e.retryAfter).toBe(17)
    expect(commandErrorMessage(e)).toBe('Too many requests, try again in 17s')
  })
  it('unknown_device', async () => {
    reply(404, { error: 'no such device', code: 'unknown_device' })
    const e = await fail(patchDevice({ prefix: 'a', device: 'b' }, { hidden: true }))
    expect(e.code).toBe('unknown_device')
    expect(commandErrorMessage(e)).toBe('Unknown device')
  })
  it('requests the admin list with include_hidden=1', async () => {
    reply(200, [])
    await fetchAllDevices()
    expect(vi.mocked(fetch).mock.calls[0][0]).toBe('/api/devices?include_hidden=1')
  })
})
