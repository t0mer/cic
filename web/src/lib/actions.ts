/** Command action names as the API, audit log and SSE report them. */
const ACTION_LABELS: Record<string, string> = {
  ping: 'Ping',
  ping_stop: 'Stop ping',
  notify: 'Notification',
  sms: 'SMS',
  getstatus: 'Refresh status',
  getlocation: 'Refresh location',
}

/** Friendly label for a command action; unknown values are humanised, never shown raw. */
export function actionLabel(action: string): string {
  const known = ACTION_LABELS[action]
  if (known) return known
  const s = action.replace(/[_/-]+/g, ' ').trim()
  return s ? s.charAt(0).toUpperCase() + s.slice(1) : 'Unknown'
}
