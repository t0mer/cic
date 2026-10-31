import { Search } from 'lucide-react'

import { SORT_KEYS, STATUS_FILTERS, type SortKey, type StatusFilter } from '@/lib/device-list'
import { cn } from '@/lib/utils'

const SORT_LABEL: Record<SortKey, string> = {
  name: 'Name',
  last_seen: 'Last seen',
  battery: 'Battery',
}
const STATUS_LABEL: Record<StatusFilter, string> = {
  all: 'All',
  online: 'Online',
  offline: 'Offline',
}

interface Props {
  query: string
  status: StatusFilter
  sort: SortKey
  count: number
  total: number
  onQuery: (q: string) => void
  onStatus: (s: StatusFilter) => void
  onSort: (s: SortKey) => void
}

export function Toolbar({ query, status, sort, count, total, onQuery, onStatus, onSort }: Props) {
  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-col gap-3 sm:flex-row">
        <label className="relative flex-1">
          <span className="sr-only">Search devices by name</span>
          <Search
            aria-hidden="true"
            className="pointer-events-none absolute start-3 top-1/2 size-4 -translate-y-1/2 text-muted-foreground"
          />
          <input
            type="search"
            dir="auto"
            value={query}
            onChange={(e) => onQuery(e.target.value)}
            placeholder="Search by name"
            className="h-11 w-full rounded-md border bg-background ps-9 pe-3 text-base focus-visible:ring-[3px] focus-visible:ring-ring focus-visible:outline-none"
          />
        </label>
        <label className="flex items-center gap-2 text-sm">
          <span className="text-muted-foreground">Sort by</span>
          <select
            value={sort}
            onChange={(e) => onSort(e.target.value as SortKey)}
            className="h-11 min-w-0 flex-1 rounded-md border bg-background px-3 text-base focus-visible:ring-[3px] focus-visible:ring-ring focus-visible:outline-none sm:flex-none"
          >
            {SORT_KEYS.map((k) => (
              <option key={k} value={k}>
                {SORT_LABEL[k]}
              </option>
            ))}
          </select>
        </label>
      </div>
      <div className="flex flex-wrap items-center gap-2">
        <div role="group" aria-label="Filter by status" className="flex gap-2">
          {STATUS_FILTERS.map((s) => (
            <button
              key={s}
              type="button"
              aria-pressed={status === s}
              onClick={() => onStatus(s)}
              className={cn(
                'inline-flex h-11 min-w-16 items-center justify-center rounded-full border px-4 text-sm font-medium focus-visible:ring-[3px] focus-visible:ring-ring focus-visible:outline-none',
                status === s
                  ? 'border-transparent bg-primary text-primary-foreground'
                  : 'bg-background hover:bg-accent',
              )}
            >
              {STATUS_LABEL[s]}
            </button>
          ))}
        </div>
        <p
          className="ms-auto text-sm text-muted-foreground"
          aria-live="polite"
          data-testid="result-count"
        >
          {count === total
            ? `${total} ${total === 1 ? 'device' : 'devices'}`
            : `${count} of ${total}`}
        </p>
      </div>
    </div>
  )
}
