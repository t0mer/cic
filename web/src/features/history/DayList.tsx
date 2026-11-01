import { Eye, EyeOff } from 'lucide-react'

import { formatClock, formatDayLabel, formatDistance, type DayRow } from '@/lib/history-days'
import { cn } from '@/lib/utils'

export function DayList({
  days,
  hidden,
  onToggle,
}: {
  days: DayRow[]
  hidden: ReadonlySet<string>
  onToggle: (date: string) => void
}) {
  return (
    <ul aria-label="Days" className="flex flex-col gap-2">
      {days.map((d) => {
        const off = hidden.has(d.date)
        const label = formatDayLabel(d.date)
        return (
          <li
            key={d.date}
            data-testid="day-row"
            className={cn(
              'flex items-center gap-3 rounded-lg border bg-card p-2 ps-3',
              off && 'opacity-60',
            )}
          >
            <span
              aria-hidden="true"
              className="h-8 w-1.5 shrink-0 rounded-full"
              style={{ backgroundColor: d.color }}
            />
            <div className="min-w-0 flex-1 text-sm">
              <p className="font-medium">{label}</p>
              <p className="text-xs text-muted-foreground">
                {formatDistance(d.distanceM)} · {d.pointCount} points · {formatClock(d.first)}–
                {formatClock(d.last)}
              </p>
            </div>
            <button
              type="button"
              aria-pressed={!off}
              aria-label={`${off ? 'Show' : 'Hide'} ${label} on the map`}
              onClick={() => onToggle(d.date)}
              className="inline-flex size-11 shrink-0 items-center justify-center rounded-md hover:bg-accent focus-visible:ring-[3px] focus-visible:ring-ring focus-visible:outline-none"
            >
              {off ? (
                <EyeOff aria-hidden="true" className="size-5" />
              ) : (
                <Eye aria-hidden="true" className="size-5" />
              )}
            </button>
          </li>
        )
      })}
    </ul>
  )
}
