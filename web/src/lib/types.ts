export interface Device {
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
  device_info: string | null
  screen_locked: boolean | null
  last_seen: number
  created_at: number
}

export interface Broker {
  connected: boolean
  host: string
  last_error: string
  since: string | null
}
