import type { Page } from '@playwright/test'

const NOW = Math.floor(Date.now() / 1000)

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

export async function mockApi(page: Page) {
  await page.route('**/api/devices', (r) => r.fulfill({ json: devices }))
  await page.route('**/api/broker', (r) => r.fulfill({ json: broker }))
  await page.route('**/api/events', (r) =>
    r.fulfill({
      status: 200,
      headers: { 'content-type': 'text/event-stream', 'cache-control': 'no-cache' },
      body: `event: hello\ndata: ${JSON.stringify(broker)}\n\n`,
    }),
  )
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
