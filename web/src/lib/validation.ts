/** Client-side checks are UX only; the server enforces every rule. */

export const MIN_PASSWORD = 12
export const DEVICE_NAME_MAX = 80
export const USERNAME_RE = /^[A-Za-z0-9._-]{3,64}$/
const HEX6 = /^#[0-9a-f]{6}$/i

const runes = (s: string) => Array.from(s).length

/** `#rrggbb` only. The empty string (automatic colour) is handled by callers. */
export const isHexColor = (v: string) => HEX6.test(v)

/** Lower-cases a valid hex colour; anything else is returned trimmed. */
export const normalizeColor = (v: string) => {
  const t = v.trim()
  return isHexColor(t) ? t.toLowerCase() : t
}

/** Empty means "automatic" and is valid. */
export const colorProblem = (v: string): string | null =>
  v.trim() === '' || isHexColor(v.trim()) ? null : 'Use a colour like #1a73e8'

/**
 * Control and format characters, line/paragraph separators, any space separator other than a plain
 * space, and characters that render as nothing (Hangul/Braille fillers).
 */
const BAD_NAME_CHAR = /[\p{Cc}\p{Cf}\p{Zl}\p{Zp}\u3164\u2800\u115F\u1160\uFFA0]|(?! )\p{Zs}/u
const VISIBLE = /[^\p{Z}\p{C}\u3164\u2800\u115F\u1160\uFFA0]/u

/** Runs of spaces collapse to one; the ends are trimmed. This is what is compared and sent. */
export const normalizeDeviceName = (v: string) => v.replace(/ {2,}/g, ' ').trim()

export const deviceNameLength = (v: string) => runes(normalizeDeviceName(v))

export const deviceNameProblem = (v: string): string | null => {
  if (BAD_NAME_CHAR.test(v))
    return 'Name cannot contain control, invisible or special space characters'
  const n = normalizeDeviceName(v)
  if (n !== '' && !VISIBLE.test(n)) return 'Name needs at least one visible character'
  return runes(n) > DEVICE_NAME_MAX ? `Name is limited to ${DEVICE_NAME_MAX} characters` : null
}

export const usernameProblem = (v: string): string | null =>
  USERNAME_RE.test(v) ? null : 'Use 3 to 64 letters, digits, dots, dashes or underscores'

/** Optional: empty means the server generates one. */
export const newUserPasswordProblem = (v: string): string | null =>
  v === '' || runes(v) >= MIN_PASSWORD
    ? null
    : `Password must be at least ${MIN_PASSWORD} characters`

export const PLACE_NAME_MAX = 40
export const PLACE_RADIUS_MIN = 10
export const PLACE_RADIUS_MAX = 50_000

export const placeNameProblem = (v: string): string | null => {
  const n = normalizeDeviceName(v)
  if (n === '') return 'Name is required'
  if (BAD_NAME_CHAR.test(v)) return 'Name cannot contain control or invisible characters'
  if (!VISIBLE.test(n)) return 'Name needs at least one visible character'
  return runes(n) > PLACE_NAME_MAX ? `Name is limited to ${PLACE_NAME_MAX} characters` : null
}

/** A number typed in a field: empty or non-numeric gives NaN. */
export const parseNumber = (v: string) => (v.trim() === '' ? NaN : Number(v.trim()))

export const latitudeProblem = (v: string): string | null => {
  const n = parseNumber(v)
  return Number.isFinite(n) && n >= -90 && n <= 90 ? null : 'Latitude must be between -90 and 90'
}

export const longitudeProblem = (v: string): string | null => {
  const n = parseNumber(v)
  return Number.isFinite(n) && n >= -180 && n <= 180
    ? null
    : 'Longitude must be between -180 and 180'
}

export const radiusProblem = (v: string): string | null => {
  const n = parseNumber(v)
  if (Number.isFinite(n) && !Number.isInteger(n)) return 'Radius must be a whole number of metres'
  return Number.isFinite(n) && n >= PLACE_RADIUS_MIN && n <= PLACE_RADIUS_MAX
    ? null
    : `Radius must be between ${PLACE_RADIUS_MIN} and ${PLACE_RADIUS_MAX} metres`
}
