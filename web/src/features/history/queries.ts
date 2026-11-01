import { useQuery } from '@tanstack/react-query'

import { fetchHistory, fetchRoutes } from '@/lib/api'

/** The most raw points the Points tab asks for; the server may return a sample when it has more. */
export const HISTORY_LIMIT = 5000

const retry = (count: number, err: Error) => !(err as { status?: number }).status && count < 2

export interface HistoryRange {
  /** RFC3339. */
  from: string
  to: string
}

// A range that reaches into the future still gains points, so it goes stale quickly; a closed
// range in the past never changes.
const staleFor = (r: HistoryRange) => (Date.parse(r.to) > Date.now() ? 30_000 : 5 * 60_000)

export const historyKey = (deviceKey: string, r: HistoryRange) =>
  ['history', deviceKey, r.from, r.to, HISTORY_LIMIT] as const
export const routesKey = (deviceKey: string, r: HistoryRange, tz: string) =>
  ['history-routes', deviceKey, r.from, r.to, tz] as const

export function useRoutes(
  prefix: string,
  device: string,
  range: HistoryRange | null,
  tz: string,
  enabled: boolean,
) {
  return useQuery({
    queryKey: routesKey(`${prefix}/${device}`, range ?? { from: '', to: '' }, tz),
    queryFn: () => fetchRoutes(prefix, device, { ...(range as HistoryRange), tz }),
    enabled: enabled && range !== null,
    staleTime: range ? staleFor(range) : 0,
    retry,
  })
}

export function useHistory(
  prefix: string,
  device: string,
  range: HistoryRange | null,
  enabled: boolean,
) {
  return useQuery({
    queryKey: historyKey(`${prefix}/${device}`, range ?? { from: '', to: '' }),
    queryFn: () =>
      fetchHistory(prefix, device, { ...(range as HistoryRange), limit: HISTORY_LIMIT }),
    enabled: enabled && range !== null,
    staleTime: range ? staleFor(range) : 0,
    retry,
  })
}
