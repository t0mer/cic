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

export type Role = 'admin' | 'viewer'

export interface Me {
  username: string
  role: Role
  must_change_password: boolean
  csrf_token: string
}

export interface LoginResponse {
  user: { username: string; role: Role; must_change_password: boolean }
  csrf_token: string
}

export type ErrorCode =
  | 'invalid'
  | 'unknown_device'
  | 'rate_limited'
  | 'sms_cap'
  | 'recipient_limited'
  | 'sms_disabled'
  | 'not_connected'
  | 'outcome_unknown'
  | 'unavailable'

export interface CommandAck {
  id: string
  action: string
  device: string
  sent_at: string | number
}

export type CommandStatus = 'confirmed_ok' | 'failed' | 'no_confirmation'

export interface CommandResult {
  id: string
  action: string
  device: string
  status: CommandStatus
  error?: string
  phone_masked?: string
}

export interface ActivityItem {
  id: string | number
  /** RFC3339 timestamp. */
  at: string
  user: string
  device: string
  action: string
  status: string
  detail: string
}

export interface ActivityPage {
  items: ActivityItem[]
  next_before: string | number | null
}
