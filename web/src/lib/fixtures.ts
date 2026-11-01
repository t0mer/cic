import type { Device } from './types'

export function makeDevice(over: Partial<Device> = {}): Device {
  return {
    key: 'dradis/pixel',
    prefix: 'dradis',
    device: 'pixel',
    full_name: 'Pixel',
    color: '',
    hidden: false,
    online: true,
    status: true,
    version: '2026.9.1',
    battery_level: 80,
    charging: false,
    charge_type: null,
    wifi_connected: true,
    ssid: 'Home',
    latitude: 32.08531,
    longitude: 34.78176,
    gps_accuracy: 10,
    location_time: 1_000_000,
    location_name: null,
    location_name_at: null,
    safe_place: null,
    device_info: 'Google Pixel 8',
    screen_locked: false,
    last_seen: 1_000_000,
    created_at: 900_000,
    ...over,
  }
}
