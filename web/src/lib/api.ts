import type {
  ActivityPage,
  AdminUser,
  Broker,
  CommandAck,
  CreatedUser,
  Device,
  DevicePatch,
  GeocodeUsage,
  HistoryResponse,
  ErrorCode,
  LoginResponse,
  SessionView,
  TwoFactorSetup,
  TwoFactorStatus,
  Me,
  Place,
  PlaceInput,
  RoutesResponse,
  UiConfig,
} from './types'
import { parseUiConfig } from './ui-config'

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
export const uiConfigKey = ['ui-config'] as const
/** Admin list including hidden devices. Under the `devices` prefix, so prefix invalidation covers both. */
export const allDevicesKey = ['devices', 'all'] as const
export const usersKey = ['users'] as const
export const geocodeUsageKey = ['geocode-usage'] as const
export const placesKey = ['places'] as const

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
  method?: 'GET' | 'POST' | 'PATCH' | 'DELETE'
  body?: unknown
  /** Do not treat a 401 as "session lost" (login, me, change-password, logout). */
  skipAuthHandler?: boolean
  /**
   * With skipAuthHandler: still treat a 401 "authentication required" (no session) as session lost,
   * while other 401s (wrong password/code) stay local.
   */
  sessionLostOnAuthRequired?: boolean
}

/** The server's message for a request that carries no valid session. */
export const AUTH_REQUIRED = 'authentication required'

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
  'forbidden_change',
  'user_exists',
  'busy',
  'not_found',
  'duplicate_name',
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
    if (
      res.status === 401 &&
      (!opts.skipAuthHandler || (opts.sessionLostOnAuthRequired && msg === AUTH_REQUIRED))
    )
      handlers?.onUnauthorized()
    if (res.status === 403 && msg === 'password change required')
      handlers?.onPasswordChangeRequired()
    if (res.status === 403 && isCsrfMessage(msg)) handlers?.onCsrfRejected?.()
    throw err
  }
  if (res.status === 204) return undefined as T
  return (await res.json()) as T
}

export const fetchDevices = () => request<Device[]>('/api/devices')
const deviceUrl = (prefix: string, device: string) =>
  `/api/devices/${encodeURIComponent(prefix)}/${encodeURIComponent(device)}`

/** Admin-only. `from`/`to` are RFC3339; the server allows at most 31 days. */
export const fetchHistory = (
  prefix: string,
  device: string,
  q: { from: string; to: string; limit: number },
) =>
  request<HistoryResponse>(
    `${deviceUrl(prefix, device)}/history?${new URLSearchParams({ from: q.from, to: q.to, limit: String(q.limit) })}`,
  )

/** Admin-only. Days are split in the IANA zone `tz`. */
export const fetchRoutes = (
  prefix: string,
  device: string,
  q: { from: string; to: string; tz: string },
) =>
  request<RoutesResponse>(
    `${deviceUrl(prefix, device)}/routes?${new URLSearchParams({ from: q.from, to: q.to, tz: q.tz })}`,
  )

export const fetchBroker = () => request<Broker>('/api/broker')
export const fetchUiConfig = async (): Promise<UiConfig> =>
  parseUiConfig(await request<unknown>('/api/ui-config'))

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

/** Second login step. `code` is a 6-digit TOTP code or a backup code. */
export const loginTwoFactor = (mfa_token: string, code: string) =>
  request<SessionView>('/api/auth/login/2fa', {
    method: 'POST',
    body: { mfa_token, code },
    skipAuthHandler: true,
  })

// Two-factor management. A wrong password/code answers 401 ("invalid credentials"/"invalid code"),
// which must not log the user out; a 401 "authentication required" is a lost session and does.
export const twoFactorKey = ['twofactor'] as const
export const fetchTwoFactor = () => request<TwoFactorStatus>('/api/auth/2fa')
export const setupTwoFactor = (current_password: string) =>
  request<TwoFactorSetup>('/api/auth/2fa/setup', {
    method: 'POST',
    body: { current_password },
    skipAuthHandler: true,
    sessionLostOnAuthRequired: true,
  })
export const enableTwoFactor = (code: string) =>
  request<{ backup_codes: string[] }>('/api/auth/2fa/enable', {
    method: 'POST',
    body: { code },
    skipAuthHandler: true,
    sessionLostOnAuthRequired: true,
  })
export const disableTwoFactor = (current_password: string, code: string) =>
  request<void>('/api/auth/2fa/disable', {
    method: 'POST',
    body: { current_password, code },
    skipAuthHandler: true,
    sessionLostOnAuthRequired: true,
  })
export const regenerateBackupCodes = (current_password: string, code: string) =>
  request<{ backup_codes: string[] }>('/api/auth/2fa/backup-codes', {
    method: 'POST',
    body: { current_password, code },
    skipAuthHandler: true,
    sessionLostOnAuthRequired: true,
  })

export const logout = () =>
  request<void>('/api/auth/logout', { method: 'POST', skipAuthHandler: true })

/** The server rotates the session: the response carries the NEW CSRF token, used from here on. */
export async function changePassword(current_password: string, new_password: string) {
  const res = await request<SessionView | undefined>('/api/auth/change-password', {
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

const devicePath = (d: Pick<Device, 'prefix' | 'device'>) =>
  `/api/devices/${encodeURIComponent(d.prefix)}/${encodeURIComponent(d.device)}`

export const fetchAllDevices = () => request<Device[]>('/api/devices?include_hidden=1')

export const patchDevice = (device: Pick<Device, 'prefix' | 'device'>, patch: DevicePatch) =>
  request<Device>(devicePath(device), { method: 'PATCH', body: patch })

export const deleteDevice = (device: Pick<Device, 'prefix' | 'device'>) =>
  request<void>(devicePath(device), { method: 'DELETE' })

export const fetchUsers = () => request<AdminUser[]>('/api/users')

export const createUser = (body: { username: string; role: string; password?: string }) =>
  request<CreatedUser>('/api/users', { method: 'POST', body })

export const patchUser = (id: number | string, patch: { role?: string; disabled?: boolean }) =>
  request<AdminUser>(`/api/users/${encodeURIComponent(String(id))}`, {
    method: 'PATCH',
    body: patch,
  })

export const resetUserPassword = (id: number | string) =>
  request<{ initial_password: string }>(
    `/api/users/${encodeURIComponent(String(id))}/reset-password`,
    { method: 'POST' },
  )

/** Rejects with a 404 ApiError until the server ships geocoding. */
export const fetchGeocodeUsage = () => request<GeocodeUsage>('/api/geocode/usage')

/** The list is a bare array; a `{places: [...]}` envelope is accepted too. */
export async function fetchPlaces(): Promise<Place[]> {
  const res = await request<Place[] | { places?: Place[] }>('/api/places')
  return Array.isArray(res) ? res : (res.places ?? [])
}

export const createPlace = (body: PlaceInput) =>
  request<Place>('/api/places', { method: 'POST', body })

export const patchPlace = (id: number, patch: Partial<PlaceInput>) =>
  request<Place>(`/api/places/${id}`, { method: 'PATCH', body: patch })

export const deletePlace = (id: number) => request<void>(`/api/places/${id}`, { method: 'DELETE' })
