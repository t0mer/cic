import { ApiError, isCsrfMessage } from './api'

/** True when a command may still be delivered although the request failed. */
export const isOutcomeUnknown = (err: unknown) =>
  err instanceof ApiError && err.code === 'outcome_unknown'

export const OUTCOME_UNKNOWN_MESSAGE =
  'No confirmation from the device. It may still arrive, so do not resend.'

/** Maps any command failure to the text shown in a toast. Never includes user content. */
export function commandErrorMessage(err: unknown): string {
  if (!(err instanceof ApiError)) return 'Could not reach the server. Check your connection.'
  switch (err.code) {
    case 'outcome_unknown':
      return OUTCOME_UNKNOWN_MESSAGE
    case 'sms_cap':
      return 'Daily SMS limit reached'
    case 'recipient_limited':
      return 'Too many messages to this number'
    case 'sms_disabled':
      return 'SMS is disabled on this server'
    case 'not_connected':
      return 'Broker not connected'
    case 'unknown_device':
      return 'Unknown device'
    case 'busy':
      return 'The server is busy, try again in a moment'
    case 'not_found':
      return 'That item no longer exists'
    case 'unavailable':
      return 'Service unavailable, try again shortly'
    case 'invalid':
    case 'forbidden_change':
    case 'user_exists':
      return err.message
    default:
      break
  }
  if (err.code === 'rate_limited' || err.status === 429)
    return err.retryAfter !== null
      ? `Too many requests, try again in ${err.retryAfter}s`
      : 'Too many requests, try again shortly'
  if (err.status === 403 && isCsrfMessage(err.message))
    return 'Session security token expired — try again'
  if (err.status === 403 && err.message === 'password change required')
    return 'You must change your password first'
  if (err.status === 403) return 'You do not have permission to do that'
  return err.message
}
