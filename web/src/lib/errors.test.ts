import { describe, expect, it } from 'vitest'

import { ApiError } from './api'
import { commandErrorMessage } from './errors'

const e = (
  status: number,
  message: string,
  code: ApiError['code'] = null,
  ra: number | null = null,
) => new ApiError(status, message, code, ra)

describe('commandErrorMessage', () => {
  it('maps codes', () => {
    expect(commandErrorMessage(e(429, 'x', 'sms_cap'))).toBe('Daily SMS limit reached')
    expect(commandErrorMessage(e(429, 'x', 'recipient_limited'))).toBe(
      'Too many messages to this number',
    )
    expect(commandErrorMessage(e(503, 'x', 'sms_disabled'))).toBe('SMS is disabled on this server')
    expect(commandErrorMessage(e(503, 'x', 'not_connected'))).toBe('Broker not connected')
    expect(commandErrorMessage(e(404, 'x', 'unknown_device'))).toBe('Unknown device')
    expect(commandErrorMessage(e(400, 'phone is bad', 'invalid'))).toBe('phone is bad')
  })
  it('maps busy and not_found', () => {
    expect(commandErrorMessage(e(503, 'x', 'busy'))).toBe(
      'The server is busy, try again in a moment',
    )
    expect(commandErrorMessage(e(404, 'x', 'not_found'))).toBe('That item no longer exists')
  })
  it('uses Retry-After for 429', () => {
    expect(commandErrorMessage(e(429, 'x', 'rate_limited', 12))).toBe(
      'Too many requests, try again in 12s',
    )
    expect(commandErrorMessage(e(429, 'x'))).toBe('Too many requests, try again shortly')
  })
  it('outcome_unknown says it may still arrive and not to resend', () => {
    const m = commandErrorMessage(e(504, 'x', 'outcome_unknown'))
    expect(m).toMatch(/may still arrive/)
    expect(m).toMatch(/do not resend/)
  })
  it('handles non-API errors and 403', () => {
    expect(commandErrorMessage(new TypeError('fetch failed'))).toMatch(/Could not reach/)
    expect(commandErrorMessage(e(403, 'forbidden'))).toMatch(/permission/)
  })
  it('CSRF 403 asks to try again; password-change 403 says so', () => {
    expect(commandErrorMessage(e(403, 'invalid csrf token'))).toBe(
      'Session security token expired — try again',
    )
    expect(commandErrorMessage(e(403, 'cross-site request rejected'))).toBe(
      'Session security token expired — try again',
    )
    expect(commandErrorMessage(e(403, 'password change required'))).toMatch(/change your password/)
  })
})
