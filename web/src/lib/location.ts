import { formatCoords } from './maps'
import { toSeconds } from './time'
import type { Device } from './types'

/** A fresh fix without a name is expected to get one within this many seconds. */
export const GEOCODE_GRACE_S = 10

export type LocationView =
  | { kind: 'unknown' }
  | { kind: 'pending'; coords: string }
  | { kind: 'named'; name: string; coords: string }
  | { kind: 'coords'; coords: string }

export function hasLocation(d: Pick<Device, 'latitude' | 'longitude'>): boolean {
  return (
    d.latitude !== null &&
    d.longitude !== null &&
    Number.isFinite(d.latitude) &&
    Number.isFinite(d.longitude)
  )
}

/**
 * What to show for a device's place. A skeleton ("pending") only while geocoding is on, the fix is
 * brand new and no name arrived yet; otherwise the name, else the plain coordinates as before.
 */
export function describeLocation(d: Device, geocodeEnabled: boolean, nowSec: number): LocationView {
  if (!hasLocation(d)) return { kind: 'unknown' }
  const coords = formatCoords(d.latitude as number, d.longitude as number)
  const name = d.location_name?.trim()
  if (name) return { kind: 'named', name, coords }
  const fresh = d.location_time ? nowSec - toSeconds(d.location_time) < GEOCODE_GRACE_S : false
  if (geocodeEnabled && fresh) return { kind: 'pending', coords }
  return { kind: 'coords', coords }
}
