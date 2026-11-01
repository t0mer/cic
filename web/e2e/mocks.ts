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
    location_name: 'Dizengoff St, Tel Aviv',
    location_name_at: iso(NOW - 100),
    safe_place: { id: 1, name: 'Home' } as { id: number; name: string } | null,
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
    location_name: null,
    location_name_at: null,
    safe_place: null,
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
    location_name: null,
    location_name_at: null,
    safe_place: null,
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

/** Tiny valid MapLibre styles with no sources or layers: the map needs no network. */
export const STYLE_LIGHT = 'https://map.example.test/styles/light'
export const STYLE_DARK = 'https://map.example.test/styles/dark'
const emptyStyle = (bg: string) => ({
  version: 8,
  sources: {},
  layers: [{ id: 'bg', type: 'background', paint: { 'background-color': bg } }],
})

export const uiConfig = {
  map: {
    provider: 'openfreemap',
    style_light: STYLE_LIGHT,
    style_dark: STYLE_DARK,
    attribution: 'Test map data',
  },
  geocode_enabled: true,
}

/** Serves the two style URLs from route handlers (never the network). */
export async function mockMapStyles(page: Page) {
  await page.route('https://map.example.test/**', (r) =>
    r.fulfill({
      json: emptyStyle(r.request().url().endsWith('/dark') ? '#1c1c1c' : '#e8eef2'),
      headers: { 'access-control-allow-origin': '*' },
    }),
  )
}

export type MockRole = 'admin' | 'viewer'

export interface Recorded {
  method: string
  path: string
  csrf: string | undefined
  body: unknown
}

export interface MockUser {
  id: number
  username: string
  role: MockRole
  disabled: boolean
  must_change_password: boolean
  created_at: string
  last_login: string | null
}

export interface MockPlace {
  id: number
  name: string
  latitude: number
  longitude: number
  radius_m: number
}

const metres = (aLat: number, aLon: number, bLat: number, bLon: number) => {
  const r = Math.PI / 180
  const h =
    Math.sin(((bLat - aLat) * r) / 2) ** 2 +
    Math.cos(aLat * r) * Math.cos(bLat * r) * Math.sin(((bLon - aLon) * r) / 2) ** 2
  return 2 * 6371000 * Math.asin(Math.sqrt(h))
}

export const GENERATED_PASSWORD = 'Gen-Pass-12345-abc'
export const RESET_PASSWORD = 'Reset-Pass-67890-xyz'

export interface MockState {
  /** Stateful device list (PATCH/DELETE change it). Hidden devices stay here. */
  devices: typeof devices
  users: MockUser[]
  /** Stateful safe places; each device's `safe_place` is derived from them on every list. */
  places: MockPlace[]
  /** null = the usage endpoint answers 404 (geocoding not shipped). */
  geocodeUsage: Record<string, unknown> | null
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
    devices: structuredClone(devices),
    places: [{ id: 1, name: 'Home', latitude: 32.0853, longitude: 34.7818, radius_m: 150 }],
    users: [
      {
        id: 1,
        username: 'tomer',
        role: 'admin',
        disabled: false,
        must_change_password: false,
        created_at: iso(NOW - 86400 * 30),
        last_login: iso(NOW - 60),
      },
      {
        id: 2,
        username: 'dana',
        role: 'viewer',
        disabled: false,
        must_change_password: false,
        created_at: iso(NOW - 86400 * 10),
        last_login: iso(NOW - 7200),
      },
    ],
    geocodeUsage: {
      enabled: true,
      calls_today: 25,
      cache_hits_today: 75,
      remaining: 75,
      daily_cap: 100,
      blocked_until: null,
      cache_hit_ratio: 0.75,
    },
  }
  let cmdSeq = 0
  let userSeq = 2
  let placeSeq = 1

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
    const isAdmin = state.user.role === 'admin'
    const forbidden = () => r.fulfill({ status: 403, json: { error: 'forbidden' } })
    const badCsrf = () => r.fulfill({ status: 403, json: { error: 'invalid csrf token' } })
    if (path === '/api/devices') {
      const all = isAdmin && url.searchParams.get('include_hidden') === '1'
      const withPlace = (d: (typeof state.devices)[number]) => {
        const hit =
          d.latitude === null || d.longitude === null
            ? undefined
            : state.places.find(
                (p) => metres(d.latitude!, d.longitude!, p.latitude, p.longitude) <= p.radius_m,
              )
        return { ...d, safe_place: hit ? { id: hit.id, name: hit.name } : null }
      }
      return r.fulfill({ json: state.devices.filter((d) => all || !d.hidden).map(withPlace) })
    }
    const dev = /^\/api\/devices\/([^/]+)\/([^/]+)$/.exec(path)
    if (dev && (method === 'PATCH' || method === 'DELETE')) {
      if (!isAdmin) return forbidden()
      if (req.headers()['x-csrf-token'] !== state.csrf) return badCsrf()
      const i = state.devices.findIndex((d) => d.key === `${dev[1]}/${dev[2]}`)
      if (i < 0)
        return r.fulfill({ status: 404, json: { error: 'unknown device', code: 'unknown_device' } })
      if (method === 'DELETE') {
        state.devices.splice(i, 1)
        return r.fulfill({ status: 204 })
      }
      const b = (body ?? {}) as { full_name?: string; color?: string; hidden?: boolean }
      const cur = state.devices[i]
      if (b.full_name !== undefined) cur.full_name = b.full_name.trim() || cur.device
      if (b.color !== undefined) {
        if (b.color !== '' && !/^#[0-9a-f]{6}$/.test(b.color))
          return r.fulfill({
            status: 400,
            json: { error: 'color must be #rrggbb', code: 'invalid' },
          })
        cur.color = b.color
      }
      if (b.hidden !== undefined) cur.hidden = b.hidden
      return r.fulfill({ json: cur })
    }
    if (path === '/api/users' || path.startsWith('/api/users/')) {
      if (!isAdmin) return forbidden()
      if (method !== 'GET' && req.headers()['x-csrf-token'] !== state.csrf) return badCsrf()
      if (path === '/api/users' && method === 'GET') return r.fulfill({ json: state.users })
      if (path === '/api/users' && method === 'POST') {
        const b = body as { username: string; role: MockRole; password?: string }
        if (state.users.some((u) => u.username === b.username))
          return r.fulfill({
            status: 409,
            json: { error: 'username already exists', code: 'user_exists' },
          })
        const user: MockUser = {
          id: ++userSeq,
          username: b.username,
          role: b.role,
          disabled: false,
          must_change_password: true,
          created_at: new Date().toISOString(),
          last_login: null,
        }
        state.users.push(user)
        return r.fulfill({
          status: 201,
          json: { user, initial_password: b.password ? null : GENERATED_PASSWORD },
        })
      }
      const um = /^\/api\/users\/(\d+)(\/reset-password)?$/.exec(path)
      const target = um ? state.users.find((u) => u.id === Number(um[1])) : undefined
      if (!um || !target)
        return r.fulfill({ status: 404, json: { error: 'unknown user', code: 'invalid' } })
      if (um[2] && method === 'POST') {
        target.must_change_password = true
        return r.fulfill({ json: { initial_password: RESET_PASSWORD } })
      }
      if (method === 'PATCH') {
        const b = body as { role?: MockRole; disabled?: boolean }
        const self = target.username === state.user.username
        if (self && (b.disabled === true || b.role === 'viewer'))
          return r.fulfill({
            status: 409,
            json: { error: 'you cannot demote or disable yourself', code: 'forbidden_change' },
          })
        if (b.role) target.role = b.role
        if (b.disabled !== undefined) target.disabled = b.disabled
        return r.fulfill({ json: target })
      }
    }
    if (path === '/api/places' || path.startsWith('/api/places/')) {
      if (!isAdmin) return forbidden()
      if (method !== 'GET' && req.headers()['x-csrf-token'] !== state.csrf) return badCsrf()
      if (path === '/api/places' && method === 'GET') return r.fulfill({ json: state.places })
      const dup = (name: string, except?: number) =>
        state.places.some(
          (p) => p.id !== except && p.name.toLowerCase() === name.trim().toLowerCase(),
        )
      const conflict = () =>
        r.fulfill({
          status: 409,
          json: { error: 'a place with that name already exists', code: 'duplicate_name' },
        })
      if (path === '/api/places' && method === 'POST') {
        const b = body as Omit<MockPlace, 'id'>
        if (dup(b.name)) return conflict()
        const place = { ...b, name: b.name.trim(), id: ++placeSeq }
        state.places.push(place)
        return r.fulfill({ status: 201, json: place })
      }
      const pm = /^\/api\/places\/(\d+)$/.exec(path)
      const i = pm ? state.places.findIndex((p) => p.id === Number(pm[1])) : -1
      if (i < 0) return r.fulfill({ status: 404, json: { error: 'not found', code: 'not_found' } })
      if (method === 'DELETE') {
        state.places.splice(i, 1)
        return r.fulfill({ status: 204 })
      }
      if (method === 'PATCH') {
        const b = body as Partial<Omit<MockPlace, 'id'>>
        if (b.name !== undefined && dup(b.name, state.places[i].id)) return conflict()
        state.places[i] = { ...state.places[i], ...b }
        return r.fulfill({ json: state.places[i] })
      }
    }
    if (path === '/api/geocode/usage') {
      if (!isAdmin) return forbidden()
      return state.geocodeUsage
        ? r.fulfill({ json: state.geocodeUsage })
        : r.fulfill({ status: 404, json: { error: 'not found' } })
    }
    if (path === '/api/broker') return r.fulfill({ json: broker })
    if (path === '/api/ui-config') return r.fulfill({ json: uiConfig })
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
