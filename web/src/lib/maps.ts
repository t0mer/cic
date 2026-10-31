export function googleMapsUrl(lat: number, lng: number): string {
  return `https://www.google.com/maps/search/?api=1&query=${lat},${lng}`
}

export function formatCoords(lat: number, lng: number): string {
  return `${lat.toFixed(4)}, ${lng.toFixed(4)}`
}
