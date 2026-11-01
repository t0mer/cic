/**
 * Google Maps search URL. Only finite numbers are interpolated, so no remote text can reach the URL.
 */
export function googleMapsUrl(lat: number, lng: number): string {
  const la = Number(lat)
  const ln = Number(lng)
  if (!Number.isFinite(la) || !Number.isFinite(ln)) return 'https://www.google.com/maps'
  return `https://www.google.com/maps/search/?api=1&query=${la},${ln}`
}

export function formatCoords(lat: number, lng: number): string {
  return `${lat.toFixed(4)}, ${lng.toFixed(4)}`
}
