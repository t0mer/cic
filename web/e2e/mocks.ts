import type { Page } from '@playwright/test'

const NOW = Math.floor(Date.now() / 1000)
const iso = (seconds: number) => new Date(seconds * 1000).toISOString()

export const devices = [
  {
    key: 'dradis/pixel',
    prefix: 'dradis',
    device: 'pixel',
    full_name: 'Tomer Pixel',
    color: '',
    hidden: false,
    online: true,
    status: true,
    version: '2026.9.1',
    battery_level: 82,
    charging: true,
    charge_type: 'AC',
    wifi_connected: true,
    ssid: 'Home-5G',
    latitude: 32.0853,
    longitude: 34.7818,
    gps_accuracy: 8,
    location_time: NOW - 120,
    device_info: 'Google Pixel 8 · Android 15',
    screen_locked: false,
    last_seen: NOW - 20,
    created_at: NOW - 86400,
  },
  {
    key: 'dradis/noa',
    prefix: 'dradis',
    device: 'noa',
    full_name: 'הטלפון של נועה',
    color: '',
    hidden: false,
    online: false,
    status: false,
    version: '2026.9.1',
    battery_level: 12,
    charging: false,
    charge_type: null,
    wifi_connected: false,
    ssid: null,
    latitude: 31.7683,
    longitude: 35.2137,
    gps_accuracy: 20,
    location_time: NOW - 7200,
    device_info: 'Samsung Galaxy S23',
    screen_locked: true,
    last_seen: NOW - 7200,
    created_at: NOW - 86400,
  },
  {
    key: 'dradis/spare',
    prefix: 'dradis',
    device: 'spare',
    full_name: 'Spare phone',
    color: '#7c3aed',
    hidden: false,
    online: false,
    status: null,
    version: null,
    battery_level: null,
    charging: null,
    charge_type: null,
    wifi_connected: null,
    ssid: null,
    latitude: null,
    longitude: null,
    gps_accuracy: null,
    location_time: null,
    device_info: null,
    screen_locked: null,
    last_seen: 0,
    created_at: NOW - 3600,
  },
]

const broker = {
  connected: true,
  host: 'mqtt.example.net:8883',
  last_error: '',
  since: new Date().toISOString(),
}

export type MockRole = 'admin' | 'viewer'

export interface Recorded {
  method: string
  path: string
  csrf: string | undefined
  body: unknown
}

export interface MockState {
  /** null = anonymous: every /api call except login answers 401. */
  user: { username: string; role: MockRole; must_change_password: boolean } | null
  /** Role granted by a successful login. */
  loginRole: MockRole
  loginMustChange: boolean
  /** Next login answers 429 with this Retry-After when set. */
  loginRateLimitSeconds: number | null
  csrf: string
  requests: Recorded[]
  /** Override the response for command POSTs. */
  commandError: { status: number; json: unknown; headers?: Record<string, string> } | null
}

export const PASSWORD = 'correct-password-123'

export const activityPages = [
  {
    items: [
      {
        id: '9',
        at: iso(NOW - 60),
        user: 'tomer',
        device: 'dradis/pixel',
        action: 'sms',
        status: 'confirmed_ok',
        detail: 'to +9725•••••567',
      },
      {
        id: '8',
        at: iso(NOW - 600),
        user: 'tomer',
        device: 'dradis/noa',
        action: 'ping',
        status: 'no_confirmation',
        detail: '',
      },
    ],
    next_before: '8',
  },
  {
    items: [
      {
        id: '7',
        at: iso(NOW - 7200),
        user: 'dana',
        device: 'dradis/pixel',
        action: 'notify',
        status: 'failed',
        detail: 'not connected',
      },
    ],
    next_before: null,
  },
]

/**
 * Mocks the whole API (auth included) with route handlers; nothing reaches a real server.
 * Mutate the returned state to change behaviour mid-test.
 */
export async function mockApi(
  page: Page,
  init: Partial<Pick<MockState, 'loginRole' | 'loginMustChange'>> & {
    role?: MockRole | null
    mustChange?: boolean
  } = {},
): Promise<MockState> {
  const role = init.role === undefined ? 'viewer' : init.role
  const state: MockState = {
    user: role ? { username: 'tomer', role, must_change_password: init.mustChange ?? false } : null,
    loginRole: init.loginRole ?? 'admin',
    loginMustChange: init.loginMustChange ?? false,
    loginRateLimitSeconds: null,
    csrf: 'csrf-token-1',
    requests: [],
    commandError: null,
  }
  let cmdSeq = 0

  await page.route(/\/api\//, async (r) => {
    const req = r.request()
    const url = new URL(req.url())
    const path = url.pathname
    const method = req.method()
    const unauth = () => r.fulfill({ status: 401, json: { error: 'unauthorized' } })
    let body: unknown = undefined
    try {
      body = req.postDataJSON()
    } catch {
      /* no body */
    }
    if (method !== 'GET')
      state.requests.push({ method, path, csrf: req.headers()['x-csrf-token'], body })

    if (path === '/api/events')
      return r.fulfill({
        status: 200,
        headers: { 'content-type': 'text/event-stream', 'cache-control': 'no-cache' },
        body: `event: hello\ndata: ${JSON.stringify(broker)}\n\n`,
      })
    if (path === '/api/auth/login') {
      if (state.loginRateLimitSeconds !== null)
        return r.fulfill({
          status: 429,
          headers: { 'Retry-After': String(state.loginRateLimitSeconds) },
          json: { error: 'too many attempts' },
        })
      const b = body as { username?: string; password?: string } | undefined
      if (b?.password !== PASSWORD)
        return r.fulfill({ status: 401, json: { error: 'invalid credentials' } })
      state.user = {
        username: b.username ?? 'tomer',
        role: state.loginRole,
        must_change_password: state.loginMustChange,
      }
      return r.fulfill({ json: { user: state.user, csrf_token: state.csrf } })
    }
    if (path === '/api/auth/me')
      return state.user ? r.fulfill({ json: { ...state.user, csrf_token: state.csrf } }) : unauth()
    if (!state.user) return unauth()
    if (path === '/api/auth/logout') {
      state.user = null
      return r.fulfill({ status: 204 })
    }
    if (path === '/api/auth/change-password') {
      state.user.must_change_password = false
      return r.fulfill({ status: 204 })
    }
    if (state.user.must_change_password)
      return r.fulfill({ status: 403, json: { error: 'password change required' } })
    if (path === '/api/devices') return r.fulfill({ json: devices })
    if (path === '/api/broker') return r.fulfill({ json: broker })
    if (path === '/api/activity') {
      if (state.user.role !== 'admin')
        return r.fulfill({ status: 403, json: { error: 'forbidden' } })
      return r.fulfill({ json: activityPages[url.searchParams.get('before') ? 1 : 0] })
    }
    const m = /^\/api\/devices\/([^/]+)\/([^/]+)\/(.+)$/.exec(path)
    if (m && method === 'POST') {
      if (state.user.role !== 'admin')
        return r.fulfill({ status: 403, json: { error: 'forbidden' } })
      if (req.headers()['x-csrf-token'] !== state.csrf)
        return r.fulfill({ status: 403, json: { error: 'csrf' } })
      if (state.commandError) return r.fulfill(state.commandError)
      cmdSeq++
      return r.fulfill({
        status: 202,
        json: { id: `cmd-${cmdSeq}`, action: m[3], device: `${m[1]}/${m[2]}`, sent_at: NOW },
      })
    }
    return r.fulfill({ status: 404, json: { error: 'not found' } })
  })
  return state
}

/**
 * Replaces EventSource with a stub that opens and stays quiet (keeps the stream "live").
 * Push server events with `emitEvent`.
 */
export async function stubEventSource(page: Page) {
  await page.addInitScript(() => {
    class Fake extends EventTarget {
      static CLOSED = 2
      readyState = 1
      onopen: ((e: Event) => void) | null = null
      onerror: ((e: Event) => void) | null = null
      constructor() {
        super()
        ;(window as unknown as { __sse?: Fake }).__sse = this
        setTimeout(() => this.onopen?.(new Event('open')), 0)
      }
      close() {
        this.readyState = 2
      }
    }
    ;(window as unknown as { EventSource: unknown }).EventSource = Fake
  })
}

/** Delivers a server-sent event through the stubbed EventSource. */
export async function emitEvent(page: Page, type: string, data: unknown) {
  await page.evaluate(
    ([t, d]) => {
      const es = (window as unknown as { __sse?: EventTarget }).__sse
      if (!es) throw new Error('stubEventSource was not installed')
      es.dispatchEvent(new MessageEvent(t, { data: JSON.stringify(d) }))
    },
    [type, data] as const,
  )
}
