/** Range selection for the history page. The URL holds local `datetime-local` strings. */

export const MAX_RANGE_DAYS = 31
const DAY_MS = 86_400_000

export const PRESETS = ['today', 'yesterday', '7d'] as const
export type Preset = (typeof PRESETS)[number]

export const PRESET_LABELS: Record<Preset, string> = {
  today: 'Today',
  yesterday: 'Yesterday',
  '7d': 'Last 7 days',
}

const pad = (n: number) => String(n).padStart(2, '0')

/** Formats a Date as a local `YYYY-MM-DDTHH:mm` value for a datetime-local input. */
export function toLocalInput(d: Date): string {
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`
}

const LOCAL = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2})$/

/** Parses a datetime-local value as local time; null when malformed or not a real date. */
export function parseLocalInput(s: string | null): Date | null {
  const m = s ? LOCAL.exec(s) : null
  if (!m) return null
  const [y, mo, d, h, mi] = m.slice(1).map(Number)
  const date = new Date(y, mo - 1, d, h, mi)
  if (date.getMonth() !== mo - 1 || date.getDate() !== d) return null
  return date
}

const startOfDay = (d: Date, offsetDays = 0) =>
  new Date(d.getFullYear(), d.getMonth(), d.getDate() + offsetDays)

/** The [from, to) window of a preset, in local time. Ends at a midnight so the key is stable. */
export function presetRange(preset: Preset, now: Date): { from: string; to: string } {
  const [a, b] = preset === 'today' ? [0, 1] : preset === 'yesterday' ? [-1, 0] : [-6, 1] // the last 7 calendar days including today
  return { from: toLocalInput(startOfDay(now, a)), to: toLocalInput(startOfDay(now, b)) }
}

export interface ResolvedRange {
  /** The values shown in the inputs. */
  fromInput: string
  toInput: string
  preset: Preset | null
  /** Set only when the range is valid. */
  from: Date | null
  to: Date | null
  error: string | null
}

/** Turns URL params into a validated range. Missing params fall back to "Today". */
export function resolveRange(params: URLSearchParams, now: Date): ResolvedRange {
  const def = presetRange('today', now)
  const fromInput = params.get('from') ?? def.from
  const toInput = params.get('to') ?? def.to
  const preset =
    PRESETS.find((p) => {
      const r = presetRange(p, now)
      return r.from === fromInput && r.to === toInput
    }) ?? null
  const from = parseLocalInput(fromInput)
  const to = parseLocalInput(toInput)
  const fail = (error: string): ResolvedRange => ({
    fromInput,
    toInput,
    preset,
    from: null,
    to: null,
    error,
  })
  if (!from || !to) return fail('Enter a valid start and end.')
  if (from.getTime() >= to.getTime()) return fail('The start must be before the end.')
  if (to.getTime() - from.getTime() > MAX_RANGE_DAYS * DAY_MS)
    return fail(`The range can be at most ${MAX_RANGE_DAYS} days.`)
  return { fromInput, toInput, preset, from, to, error: null }
}

/** Returns new params with the given range ends set; an empty value drops the param (default). */
export function withRange(
  prev: URLSearchParams,
  range: { from?: string; to?: string },
): URLSearchParams {
  const next = new URLSearchParams(prev)
  for (const k of ['from', 'to'] as const) {
    const v = range[k]
    if (v === undefined) continue
    if (v) next.set(k, v)
    else next.delete(k)
  }
  return next
}

/** The browser's IANA time zone (the server splits days in it). */
export const browserTimeZone = (): string => {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC'
  } catch {
    return 'UTC'
  }
}
