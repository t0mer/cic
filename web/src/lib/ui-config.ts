import type { UiConfig } from './types'

function httpsUrl(v: unknown, field: string): string {
  if (typeof v !== 'string' || v.trim() === '') throw new Error(`ui-config: ${field} is missing`)
  let u: URL
  try {
    u = new URL(v)
  } catch {
    throw new Error(`ui-config: ${field} is not a URL`)
  }
  if (u.protocol !== 'https:') throw new Error(`ui-config: ${field} must be an https URL`)
  return v
}

/** Validates the /api/ui-config response. Style URLs stay opaque but must be https. */
export function parseUiConfig(raw: unknown): UiConfig {
  if (!raw || typeof raw !== 'object') throw new Error('ui-config: unexpected response')
  const o = raw as Record<string, unknown>
  const m = o.map
  if (!m || typeof m !== 'object') throw new Error('ui-config: map is missing')
  const map = m as Record<string, unknown>
  return {
    map: {
      provider: typeof map.provider === 'string' ? map.provider : '',
      style_light: httpsUrl(map.style_light, 'style_light'),
      style_dark: httpsUrl(map.style_dark, 'style_dark'),
      attribution: typeof map.attribution === 'string' ? map.attribution : '',
    },
    geocode_enabled: o.geocode_enabled === true,
  }
}

/** MapLibre writes custom attribution with innerHTML, so server text is escaped to plain text. */
export function escapeHtml(s: string): string {
  return s
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#39;')
}
