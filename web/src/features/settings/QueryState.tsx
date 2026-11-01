import type { UseQueryResult } from '@tanstack/react-query'
import type { ReactNode } from 'react'

import { Button } from '@/components/ui/button'
import { commandErrorMessage } from '@/lib/errors'

/** Loading / error / empty wrapper for the list sections. */
export function QueryState<T>({
  query,
  what,
  empty,
  children,
}: {
  query: UseQueryResult<T[]>
  what: string
  empty: string
  children: (data: T[]) => ReactNode
}) {
  if (query.isPending)
    return (
      <div role="status" aria-busy="true" className="flex flex-col gap-2">
        <span className="sr-only">Loading {what}</span>
        {[0, 1, 2].map((i) => (
          <div key={i} className="h-16 animate-pulse rounded-lg bg-accent" />
        ))}
      </div>
    )
  if (query.error && !query.data)
    return (
      <div className="flex flex-col items-start gap-2 rounded-lg border border-dashed p-4">
        <p role="alert" className="text-sm text-destructive">
          Could not load {what}: {commandErrorMessage(query.error)}
        </p>
        <Button variant="outline" className="h-11 px-4" onClick={() => void query.refetch()}>
          Retry
        </Button>
      </div>
    )
  const data = query.data ?? []
  if (data.length === 0)
    return (
      <p className="rounded-lg border border-dashed p-6 text-center text-sm text-muted-foreground">
        {empty}
      </p>
    )
  return <>{children(data)}</>
}
