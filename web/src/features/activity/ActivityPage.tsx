import { useInfiniteQuery } from '@tanstack/react-query'
import { AlertTriangle, Inbox, Lock } from 'lucide-react'

import { Button } from '@/components/ui/button'
import { useAuth } from '@/features/auth/auth-context'
import { useDevices } from '@/features/devices/queries'
import { activityKey, fetchActivity } from '@/lib/api'
import { useNow } from '@/lib/now'
import { actionLabel } from '@/lib/actions'
import { relativeTime } from '@/lib/time'
import type { ActivityItem, Device } from '@/lib/types'
import { cn } from '@/lib/utils'

/** `at` is an RFC3339 string; an unparsable value maps to 0 (shown as a very old time). */
export function atToSeconds(at: string): number {
  const t = Date.parse(at)
  return Number.isNaN(t) ? 0 : Math.floor(t / 1000)
}

const STATUS: Record<string, { label: string; cls: string }> = {
  confirmed_ok: {
    label: 'Confirmed',
    cls: 'bg-green-100 text-green-900 dark:bg-green-950 dark:text-green-300',
  },
  failed: { label: 'Failed', cls: 'bg-red-100 text-red-900 dark:bg-red-950 dark:text-red-300' },
  no_confirmation: {
    label: 'No confirmation',
    cls: 'bg-amber-100 text-amber-900 dark:bg-amber-950 dark:text-amber-300',
  },
}

function StatusBadge({ status }: { status: string }) {
  const s = STATUS[status] ?? { label: actionLabel(status), cls: 'bg-muted text-muted-foreground' }
  return (
    <span
      className={cn('inline-flex shrink-0 rounded-full px-2.5 py-1 text-xs font-medium', s.cls)}
    >
      {s.label}
    </span>
  )
}

function deviceName(devices: Device[] | undefined, id: string): string {
  const d = devices?.find((x) => x.key === id)
  return d ? d.full_name || d.device : id
}

function Message({
  icon,
  title,
  children,
}: {
  icon: React.ReactNode
  title: string
  children?: React.ReactNode
}) {
  return (
    <div className="flex flex-col items-center gap-3 rounded-xl border border-dashed p-8 text-center">
      <div aria-hidden="true" className="text-muted-foreground [&>svg]:size-10">
        {icon}
      </div>
      <h2 className="text-lg font-semibold">{title}</h2>
      {children}
    </div>
  )
}

function Row({ item, devices, now }: { item: ActivityItem; devices?: Device[]; now: number }) {
  return (
    <li className="flex flex-col gap-1 rounded-lg border bg-card p-3 text-card-foreground">
      <div className="flex items-center justify-between gap-2">
        <span className="font-medium">{actionLabel(item.action)}</span>
        <StatusBadge status={item.status} />
      </div>
      <div className="flex flex-wrap items-center gap-x-2 text-sm text-muted-foreground">
        <span dir="auto">{item.user}</span>
        <span aria-hidden="true">·</span>
        <span dir="auto">{deviceName(devices, item.device)}</span>
        <span aria-hidden="true">·</span>
        <span>{relativeTime(atToSeconds(item.at), now)}</span>
      </div>
      {item.detail ? (
        <p dir="auto" className="text-sm break-words">
          {item.detail}
        </p>
      ) : null}
    </li>
  )
}

function Feed() {
  const now = useNow()
  const { data: devices } = useDevices()
  const q = useInfiniteQuery({
    queryKey: activityKey,
    queryFn: ({ pageParam }) => fetchActivity(pageParam),
    initialPageParam: null as string | number | null,
    getNextPageParam: (last) => last.next_before ?? undefined,
    retry: (count, err) => !(err as { status?: number }).status && count < 2,
  })

  if (q.isPending)
    return (
      <div role="status" aria-busy="true" className="flex flex-col gap-2">
        <span className="sr-only">Loading activity</span>
        {Array.from({ length: 5 }, (_, i) => (
          <div key={i} aria-hidden="true" className="h-20 animate-pulse rounded-lg bg-accent" />
        ))}
      </div>
    )
  if (q.isError && !q.data)
    return (
      <Message icon={<AlertTriangle />} title="Could not load activity">
        <p role="alert" className="text-sm text-muted-foreground">
          {q.error.message}
        </p>
        <Button variant="outline" className="h-11 px-6" onClick={() => void q.refetch()}>
          Retry
        </Button>
      </Message>
    )
  const items = q.data?.pages.flatMap((p) => p.items) ?? []
  if (items.length === 0) return <Message icon={<Inbox />} title="No activity yet" />
  return (
    <div className="flex flex-col gap-3">
      <ul className="flex flex-col gap-2" data-testid="activity-list">
        {items.map((it) => (
          <Row key={String(it.id)} item={it} devices={devices} now={now} />
        ))}
      </ul>
      {q.isFetchNextPageError ? (
        <p role="alert" className="text-sm text-destructive">
          Could not load more: {q.error?.message}
        </p>
      ) : null}
      {q.hasNextPage ? (
        <Button
          variant="outline"
          className="h-11 self-center px-6"
          onClick={() => void q.fetchNextPage()}
          disabled={q.isFetchingNextPage}
        >
          {q.isFetchingNextPage ? 'Loading…' : 'Load more'}
        </Button>
      ) : null}
    </div>
  )
}

export function ActivityPage() {
  const { isAdmin } = useAuth()
  return (
    <div className="flex flex-col gap-4">
      <h1 className="text-2xl font-semibold">Activity</h1>
      {isAdmin ? <Feed /> : <Message icon={<Lock />} title="Not available for your role" />}
    </div>
  )
}
