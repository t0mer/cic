import type { Device } from './types'

export type StatusFilter = 'all' | 'online' | 'offline'
export type SortKey = 'name' | 'last_seen' | 'battery'

export const STATUS_FILTERS: StatusFilter[] = ['all', 'online', 'offline']
export const SORT_KEYS: SortKey[] = ['name', 'last_seen', 'battery']

const fold = (s: string) => s.normalize('NFKC').toLocaleLowerCase()

export function matchesSearch(d: Device, query: string): boolean {
  const q = fold(query.trim())
  if (!q) return true
  return (
    fold(d.full_name).includes(q) ||
    fold(d.device).includes(q) ||
    fold(d.location_name ?? '').includes(q)
  )
}

export function filterDevices(devices: Device[], query: string, status: StatusFilter): Device[] {
  return devices.filter(
    (d) =>
      !d.hidden &&
      matchesSearch(d, query) &&
      (status === 'all' || (status === 'online' ? d.online : !d.online)),
  )
}

const byName = (a: Device, b: Device) =>
  a.full_name.localeCompare(b.full_name, undefined, { sensitivity: 'base', numeric: true }) ||
  a.key.localeCompare(b.key)

/** Name: A-Z. Last seen: newest first, never-seen last. Battery: lowest first, unknown last. */
export function sortDevices(devices: Device[], key: SortKey): Device[] {
  const out = [...devices]
  out.sort((a, b) => {
    if (key === 'last_seen') {
      const x = a.last_seen > 0 ? a.last_seen : -Infinity
      const y = b.last_seen > 0 ? b.last_seen : -Infinity
      if (x !== y) return x === -Infinity ? 1 : y === -Infinity ? -1 : y - x
    } else if (key === 'battery') {
      const x = a.battery_level
      const y = b.battery_level
      if (x !== y) {
        if (x === null) return 1
        if (y === null) return -1
        return x - y
      }
    }
    return byName(a, b)
  })
  return out
}
