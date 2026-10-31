const GSM_BASIC = new Set(
  Array.from(
    '@£$¥èéùìòÇ\nØø\rÅåΔ_ΦΓΛΩΠΨΣΘΞÆæßÉ !"#¤%&\'()*+,-./0123456789:;<=>?¡ABCDEFGHIJKLMNOPQRSTUVWXYZÄÖÑÜ§¿abcdefghijklmnopqrstuvwxyzäöñüà',
  ),
)
/** Extension table characters: two septets each. */
const GSM_EXT = new Set(['^', '{', '}', '\\', '[', '~', ']', '|', '€'])

export interface SmsInfo {
  /** User-perceived length in code points. */
  characters: number
  /** Septets (GSM-7) or UTF-16 code units (Unicode) used. */
  units: number
  segments: number
  encoding: 'GSM-7' | 'Unicode'
}

function analyse(text: string) {
  const chars = Array.from(text)
  const gsm = chars.every((c) => GSM_BASIC.has(c) || GSM_EXT.has(c))
  const cost = (c: string) => (gsm ? (GSM_EXT.has(c) ? 2 : 1) : c.length)
  return { chars, gsm, cost }
}

/** Splits text into SMS parts, never splitting an extension pair or a surrogate pair. */
export function smsParts(text: string): string[] {
  const { chars, gsm, cost } = analyse(text)
  const total = chars.reduce((n, c) => n + cost(c), 0)
  if (total === 0) return []
  if (total <= (gsm ? 160 : 70)) return [text]
  const cap = gsm ? 153 : 67
  const parts: string[] = []
  let cur = ''
  let used = 0
  for (const c of chars) {
    const w = cost(c)
    if (used + w > cap) {
      parts.push(cur)
      cur = ''
      used = 0
    }
    cur += c
    used += w
  }
  parts.push(cur)
  return parts
}

export function smsSegments(text: string): SmsInfo {
  const { chars, gsm, cost } = analyse(text)
  return {
    characters: chars.length,
    units: chars.reduce((n, c) => n + cost(c), 0),
    segments: smsParts(text).length,
    encoding: gsm ? 'GSM-7' : 'Unicode',
  }
}

const E164 = /^\+[1-9]\d{6,14}$/
export const isValidE164 = (phone: string) => E164.test(phone)

/** UX convenience: drop spaces and hyphens a user may paste. */
export const normalizePhone = (s: string) => s.trim().replace(/[\s-]/g, '')

/** "+972501234567" -> "+9725•••••567" */
export function maskPhone(phone: string): string {
  const head = phone.length >= 9 ? 5 : 2
  const tail = phone.length >= 9 ? 3 : 2
  const hidden = Math.max(1, phone.length - head - tail)
  return phone.slice(0, head) + '•'.repeat(hidden) + phone.slice(-tail)
}

/** Control (except newline) and format characters are rejected client-side as UX. */
export const hasForbiddenChars = (s: string) => /[\p{Cc}\p{Cf}]/u.test(s.replace(/\n/g, ''))

export const countChars = (s: string) => Array.from(s).length
