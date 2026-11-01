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
  /** Reverse-geocoded place name, or null when not (yet) resolved. */
  location_name: string | null
  /** RFC3339 time the name was resolved. */
  location_name_at: string | null
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
  | 'forbidden_change'
  | 'user_exists'
  | 'busy'
  | 'not_found'

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

export interface UiConfig {
  map: {
    provider: string
    style_light: string
    style_dark: string
    attribution: string
  }
  geocode_enabled: boolean
}

export interface AdminUser {
  id: number | string
  username: string
  role: Role
  disabled: boolean
  must_change_password: boolean
  /** RFC3339. */
  created_at: string
  /** RFC3339, or null when the user never signed in. */
  last_login: string | null
}

/** `initial_password` is only ever held in handler/component state, never in a query cache. */
export interface CreatedUser {
  user: AdminUser
  initial_password: string | null
}

export interface DevicePatch {
  full_name?: string
  color?: string
  hidden?: boolean
}

export interface GeocodeUsage {
  enabled: boolean
  calls_today: number
  cache_hits_today: number
  remaining: number | null
  daily_cap: number
  blocked_until: string | null
  cache_hit_ratio: number
}
