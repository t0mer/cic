/** Unix seconds; values that look like milliseconds are normalised. */
export function toSeconds(ts: number): number {
  return ts > 1e11 ? Math.floor(ts / 1000) : ts
}

export function relativeTime(ts: number | null | undefined, nowSec: number): string {
  if (!ts || ts <= 0) return 'never'
  const d = Math.max(0, nowSec - toSeconds(ts))
  if (d < 45) return 'just now'
  if (d < 90) return '1 min ago'
  if (d < 45 * 60) return `${Math.round(d / 60)} min ago`
  if (d < 90 * 60) return '1 hour ago'
  if (d < 22 * 3600) return `${Math.round(d / 3600)} hours ago`
  if (d < 36 * 3600) return '1 day ago'
  if (d < 30 * 86400) return `${Math.round(d / 86400)} days ago`
  if (d < 45 * 86400) return '1 month ago'
  if (d < 365 * 86400) return `${Math.round(d / (30 * 86400))} months ago`
  return `${Math.max(1, Math.round(d / (365 * 86400)))} years ago`
}
