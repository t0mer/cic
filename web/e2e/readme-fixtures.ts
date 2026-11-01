/**
 * Fictional fixtures for the README screenshots. Nothing here is real: names, places, SSIDs,
 * numbers and hosts are invented. Wraps `mockApi` instead of changing it, so the e2e suites keep
 * their own data.
 */
import type { Page } from '@playwright/test'

import {
  mockApi,
  mockMapStyles,
  type MockHistoryPoint,
  type MockState,
  type MockUser,
} from './mocks'

export const OPENFREEMAP_LIGHT = 'https://tiles.openfreemap.org/styles/positron'
export const OPENFREEMAP_DARK = 'https://tiles.openfreemap.org/styles/dark'

const NOW = Math.floor(Date.now() / 1000)
const iso = (seconds: number) => new Date(seconds * 1000).toISOString()

interface Dev {
  key: string
  prefix: string
  device: string
  full_name: string
  color: string
  hidden: boolean
  online: boolean
  status: boolean | null
  version: string | null
  battery_level: number | null
  charging: boolean | null
  charge_type: string | null
  wifi_connected: boolean | null
  ssid: string | null
  latitude: number | null
  longitude: number | null
  gps_accuracy: number | null
  location_time: number | null
  location_name: string | null
  location_name_at: string | null
  safe_place: { id: number; name: string } | null
  device_info: string | null
  screen_locked: boolean | null
  last_seen: number
  created_at: number
}

const base: Dev = {
  key: '',
  prefix: 'dradis',
  device: '',
  full_name: '',
  color: '',
  hidden: false,
  online: true,
  status: true,
  version: '2026.10.0',
  battery_level: 80,
  charging: false,
  charge_type: null,
  wifi_connected: true,
  ssid: 'Home-WiFi',
  latitude: null,
  longitude: null,
  gps_accuracy: 10,
  location_time: NOW - 90,
  location_name: null,
  location_name_at: null,
  safe_place: null,
  device_info: null,
  screen_locked: false,
  last_seen: NOW - 15,
  created_at: NOW - 86400 * 20,
}

export const readmeDevices: Dev[] = [
  {
    ...base,
    key: 'dradis/alex-pixel',
    device: 'alex-pixel',
    full_name: 'Alex Pixel',
    battery_level: 84,
    charging: true,
    charge_type: 'AC',
    latitude: 32.0636,
    longitude: 34.7745,
    gps_accuracy: 6,
    location_name: 'Rothschild Blvd, Tel Aviv',
    location_name_at: iso(NOW - 80),
    device_info: 'Google Pixel 8 · Android 15',
  },
  {
    ...base,
    key: 'dradis/dana-galaxy',
    device: 'dana-galaxy',
    full_name: 'Dana Galaxy',
    color: '#7c3aed',
    battery_level: 57,
    ssid: null,
    wifi_connected: false,
    latitude: 32.0795,
    longitude: 34.7815,
    gps_accuracy: 12,
    location_time: NOW - 240,
    location_name: 'Ibn Gabirol St, Tel Aviv',
    location_name_at: iso(NOW - 200),
    device_info: 'Samsung Galaxy S24 · Android 15',
    last_seen: NOW - 40,
  },
  {
    ...base,
    key: 'dradis/sam-moto',
    device: 'sam-moto',
    full_name: 'Sam Moto',
    online: false,
    status: false,
    battery_level: 18,
    wifi_connected: false,
    ssid: null,
    latitude: 32.0714,
    longitude: 34.7699,
    gps_accuracy: 25,
    location_time: NOW - 5400,
    location_name: 'Allenby St, Tel Aviv',
    location_name_at: iso(NOW - 5300),
    screen_locked: true,
    device_info: 'Motorola Moto G84 · Android 14',
    last_seen: NOW - 5400,
  },
]

export const readmePlaces = [
  { id: 1, name: 'Home', latitude: 32.0636, longitude: 34.7745, radius_m: 150 },
  { id: 2, name: 'Work', latitude: 32.0795, longitude: 34.7815, radius_m: 200 },
]

export const readmeUsers: MockUser[] = [
  {
    id: 1,
    username: 'admin',
    role: 'admin',
    disabled: false,
    must_change_password: false,
    created_at: iso(NOW - 86400 * 30),
    last_login: iso(NOW - 120),
  },
  {
    id: 2,
    username: 'guest',
    role: 'viewer',
    disabled: false,
    must_change_password: false,
    created_at: iso(NOW - 86400 * 9),
    last_login: iso(NOW - 86400 * 2),
  },
]

type LatLon = [number, number]
const HOME: LatLon = [32.0636, 34.7745]
const WORK: LatLon = [32.0795, 34.7815]
const CAFE: LatLon = [32.0733, 34.7745]
const PARK: LatLon = [32.0692, 34.7669]

/** Walks the waypoints, one point every `stepS` seconds from `start`, with a little jitter. */
function leg(start: number, via: LatLon[], stepS: number, seed: number): MockHistoryPoint[] {
  const len = (a: LatLon, b: LatLon) => Math.hypot(b[0] - a[0], b[1] - a[1])
  const total = via.slice(1).reduce((n, p, i) => n + len(via[i], p), 0)
  const n = Math.max(6, Math.round(total / 0.00045))
  const out: MockHistoryPoint[] = []
  for (let k = 0; k <= n; k++) {
    let d = (k / n) * total
    let i = 0
    while (i < via.length - 2 && d > len(via[i], via[i + 1])) d -= len(via[i++], via[i])
    const f = Math.min(1, d / len(via[i], via[i + 1]))
    const j = Math.sin((k + seed) * 1.7) * 0.00005
    out.push({
      t: start + k * stepS,
      lat: via[i][0] + (via[i + 1][0] - via[i][0]) * f + j,
      lon: via[i][1] + (via[i + 1][1] - via[i][1]) * f - j,
      accuracy: 8 + ((k + seed) % 9),
    })
  }
  return out
}

/** Four past days; each has 3-4 separate trips (gaps well above the 15 min segment split). */
export function readmeHistory(): MockHistoryPoint[] {
  const out: MockHistoryPoint[] = []
  const today = new Date()
  for (let back = 4; back >= 1; back--) {
    const at = (h: number, m = 0) =>
      Math.floor(
        new Date(today.getFullYear(), today.getMonth(), today.getDate() - back, h, m).getTime() /
          1000,
      )
    const s = back * 3
    out.push(...leg(at(8, 5), [HOME, [32.069, 34.7745], CAFE, [32.076, 34.779], WORK], 150, s))
    out.push(...leg(at(12, 30), [WORK, [32.077, 34.776], CAFE, WORK], 180, s + 1))
    out.push(...leg(at(17, 40), [WORK, [32.076, 34.779], PARK, [32.065, 34.77], HOME], 200, s + 2))
    if (back % 2 === 0) out.push(...leg(at(20, 10), [HOME, PARK, CAFE, HOME], 210, s + 3))
  }
  return out
}

export interface ReadmeOptions {
  role?: 'admin' | 'viewer' | null
  /** Real OpenFreeMap styles (needs the network) instead of the blank mocked styles. */
  realTiles: boolean
  twoFactor?: boolean
}

/** Mocks the API with the fictional data above. Returns the mock state. */
export async function readmeApi(page: Page, opts: ReadmeOptions): Promise<MockState> {
  const state = await mockApi(page, { role: opts.role === undefined ? 'admin' : opts.role })
  state.devices = structuredClone(readmeDevices) as unknown as MockState['devices']
  state.places = structuredClone(readmePlaces)
  state.users = structuredClone(readmeUsers)
  state.history = { 'dradis/alex-pixel': readmeHistory() }
  if (state.user) state.user.username = 'admin'
  if (opts.twoFactor) {
    state.twoFactor = {
      enabled: true,
      pending: false,
      backupCodes: Array.from({ length: 10 }, () => 'AAAAA-BBBBB'),
    }
  }

  if (opts.realTiles) {
    await page.route('**/api/ui-config', (r) =>
      r.fulfill({
        json: {
          map: {
            provider: 'openfreemap',
            style_light: OPENFREEMAP_LIGHT,
            style_dark: OPENFREEMAP_DARK,
            attribution: '© OpenStreetMap contributors · OpenFreeMap',
          },
          geocode_enabled: true,
          history_enabled: true,
        },
      }),
    )
  } else {
    await mockMapStyles(page)
  }
  const broker = {
    connected: true,
    host: 'mqtt.example.local:1883',
    last_error: '',
    since: new Date().toISOString(),
  }
  await page.route('**/api/broker', (r) => r.fulfill({ json: broker }))

  // Routes with several segments per day: split wherever two points are > 15 min apart.
  await page.route(/\/api\/devices\/[^/]+\/[^/]+\/routes/, async (r) => {
    const url = new URL(r.request().url())
    const key = /\/devices\/([^/]+)\/([^/]+)\//.exec(url.pathname)!.slice(1, 3).join('/')
    const from = Date.parse(url.searchParams.get('from') ?? '')
    const to = Date.parse(url.searchParams.get('to') ?? '')
    const tz = url.searchParams.get('tz') || 'UTC'
    const fmt = new Intl.DateTimeFormat('en-CA', { timeZone: tz })
    const pts = (state.history[key] ?? []).filter((p) => p.t * 1000 >= from && p.t * 1000 < to)
    const byDay = new Map<string, MockHistoryPoint[]>()
    for (const p of pts) {
      const d = fmt.format(new Date(p.t * 1000))
      byDay.set(d, [...(byDay.get(d) ?? []), p])
    }
    const rad = Math.PI / 180
    const dist = (a: MockHistoryPoint, b: MockHistoryPoint) => {
      const h =
        Math.sin(((b.lat - a.lat) * rad) / 2) ** 2 +
        Math.cos(a.lat * rad) * Math.cos(b.lat * rad) * Math.sin(((b.lon - a.lon) * rad) / 2) ** 2
      return 2 * 6371000 * Math.asin(Math.sqrt(h))
    }
    const days = [...byDay.entries()].map(([date, ps]) => {
      const segments: number[][][] = []
      let cur: number[][] = []
      let distance = 0
      ps.forEach((p, i) => {
        if (i > 0 && p.t - ps[i - 1].t > 900) {
          segments.push(cur)
          cur = []
        } else if (i > 0) distance += dist(ps[i - 1], p)
        cur.push([p.lat, p.lon, p.t])
      })
      segments.push(cur)
      return {
        date,
        distance_m: distance,
        point_count: ps.length,
        first: ps[0].t,
        last: ps[ps.length - 1].t,
        segments,
      }
    })
    await r.fulfill({ json: { days } })
  })
  return state
}
