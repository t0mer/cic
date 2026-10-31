import type {
  ActivityPage,
  Broker,
  CommandAck,
  Device,
  ErrorCode,
  LoginResponse,
  Me,
} from './types'

export class ApiError extends Error {
  readonly status: number
  readonly code: ErrorCode | null
  /** Seconds from a Retry-After header, when present and numeric. */
  readonly retryAfter: number | null
  constructor(
    status: number,
    message: string,
    code: ErrorCode | null = null,
    retryAfter: number | null = null,
  ) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.code = code
    this.retryAfter = retryAfter
  }
}

export const devicesKey = ['devices'] as const
export const brokerKey = ['broker'] as const
export const authKey = ['auth', 'me'] as const
export const activityKey = ['activity'] as const

// The CSRF token lives in module memory only: never in storage, cookies or URLs.
let csrfToken: string | null = null
export const setCsrfToken = (t: string | null) => {
  csrfToken = t
}
export const getCsrfToken = () => csrfToken

// Bumped whenever the session is replaced or dropped, so a response that was already in flight
// (e.g. /me during logout) can tell it is stale and must not touch the CSRF token.
let sessionEpoch = 0
export const bumpSession = () => ++sessionEpoch
export const currentSession = () => sessionEpoch

interface AuthHandlers {
  onUnauthorized: () => void
  onPasswordChangeRequired: () => void
  /** The CSRF token was rejected: re-read the session to get a fresh one. */
  onCsrfRejected?: () => void
}

/** True for the 403 messages the server sends when the CSRF check fails. */
export const isCsrfMessage = (msg: string) => {
  const m = msg.toLowerCase()
  return m.includes('invalid csrf token') || m.includes('cross-site request rejected')
}
let handlers: AuthHandlers | null = null
export const setAuthHandlers = (h: AuthHandlers | null) => {
  handlers = h
}

interface RequestOptions {
  method?: 'GET' | 'POST'
  body?: unknown
  /** Do not treat a 401 as "session lost" (login, me, change-password, logout). */
  skipAuthHandler?: boolean
}

const CODES: readonly string[] = [
  'invalid',
  'unknown_device',
  'rate_limited',
  'sms_cap',
  'recipient_limited',
  'sms_disabled',
  'not_connected',
  'outcome_unknown',
  'unavailable',
]

async function request<T>(url: string, opts: RequestOptions = {}): Promise<T> {
  const method = opts.method ?? 'GET'
  const headers: Record<string, string> = { Accept: 'application/json' }
  if (method !== 'GET') {
    if (csrfToken) headers['X-CSRF-Token'] = csrfToken
    if (opts.body !== undefined) headers['Content-Type'] = 'application/json'
  }
  const res = await fetch(url, {
    method,
    headers,
    credentials: 'same-origin',
    body: opts.body === undefined ? undefined : JSON.stringify(opts.body),
  })
  if (!res.ok) {
    let msg = `Request failed (${res.status})`
    let code: ErrorCode | null = null
    try {
      const body: unknown = await res.json()
      if (body && typeof body === 'object') {
        if ('error' in body && typeof body.error === 'string') msg = body.error
        if ('code' in body && typeof body.code === 'string' && CODES.includes(body.code))
          code = body.code as ErrorCode
      }
    } catch {
      /* non-JSON error body */
    }
    const ra = Number.parseInt(res.headers.get('Retry-After') ?? '', 10)
    const err = new ApiError(res.status, msg, code, Number.isFinite(ra) && ra >= 0 ? ra : null)
    if (res.status === 401 && !opts.skipAuthHandler) handlers?.onUnauthorized()
    if (res.status === 403 && msg === 'password change required')
      handlers?.onPasswordChangeRequired()
    if (res.status === 403 && isCsrfMessage(msg)) handlers?.onCsrfRejected?.()
    throw err
  }
  if (res.status === 204) return undefined as T
  return (await res.json()) as T
}

export const fetchDevices = () => request<Device[]>('/api/devices')
export const fetchBroker = () => request<Broker>('/api/broker')

/** Resolves to null (not an error) when anonymous. */
export async function fetchMe(): Promise<Me | null> {
  const epoch = sessionEpoch
  try {
    const me = await request<Me>('/api/auth/me', { skipAuthHandler: true })
    // The session changed while this was in flight (logout, new login): drop the stale answer.
    if (epoch !== sessionEpoch) return null
    setCsrfToken(me.csrf_token)
    return me
  } catch (e) {
    if (e instanceof ApiError && e.status === 401) {
      if (epoch === sessionEpoch) setCsrfToken(null)
      return null
    }
    throw e
  }
}

export const login = (username: string, password: string) =>
  request<LoginResponse>('/api/auth/login', {
    method: 'POST',
    body: { username, password },
    skipAuthHandler: true,
  })

export const logout = () =>
  request<void>('/api/auth/logout', { method: 'POST', skipAuthHandler: true })

/** The server rotates the session: the response carries the NEW CSRF token, used from here on. */
export async function changePassword(current_password: string, new_password: string) {
  const res = await request<LoginResponse | undefined>('/api/auth/change-password', {
    method: 'POST',
    body: { current_password, new_password },
    skipAuthHandler: true,
  })
  if (res?.csrf_token) setCsrfToken(res.csrf_token)
  return res
}

/** URL path segments of the command endpoints. */
export type CommandAction =
  'ping' | 'ping/stop' | 'notify' | 'sms' | 'refresh-status' | 'refresh-location'

const outcomeUnknown = (cause: ApiError | null) =>
  new ApiError(cause?.status ?? 0, cause?.message ?? 'No response', 'outcome_unknown', null)

/**
 * A command POST whose fate is unclear (no response at all, or a 5xx the server did not classify)
 * may still have been delivered, so it is reported exactly like `outcome_unknown`.
 */
export async function sendCommand(
  device: Pick<Device, 'prefix' | 'device'>,
  action: CommandAction,
  body?: unknown,
): Promise<CommandAck> {
  try {
    return await request<CommandAck>(
      `/api/devices/${encodeURIComponent(device.prefix)}/${encodeURIComponent(device.device)}/${action}`,
      { method: 'POST', body },
    )
  } catch (e) {
    if (!(e instanceof ApiError)) throw outcomeUnknown(null)
    if (e.status >= 500 && e.code === null) throw outcomeUnknown(e)
    throw e
  }
}

export const fetchActivity = (before?: string | number | null, limit = 50) => {
  const q = new URLSearchParams({ limit: String(limit) })
  if (before !== undefined && before !== null) q.set('before', String(before))
  return request<ActivityPage>(`/api/activity?${q.toString()}`)
}
