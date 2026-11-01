import { ChevronLeft, ChevronRight } from 'lucide-react'
import { useState } from 'react'

import { Button } from '@/components/ui/button'
import { formatDateTime } from '@/lib/history-days'
import type { HistoryPoint } from '@/lib/types'

export const PAGE_SIZE = 50

export function PointsTable({ points }: { points: HistoryPoint[] }) {
  const [page, setPage] = useState(0)
  const pages = Math.max(1, Math.ceil(points.length / PAGE_SIZE))
  // The data can shrink under us (new range): never sit past the last page.
  const cur = Math.min(page, pages - 1)
  const rows = points.slice(cur * PAGE_SIZE, (cur + 1) * PAGE_SIZE)
  return (
    <div className="flex flex-col gap-3">
      <div className="overflow-x-auto rounded-lg border">
        <table className="w-full text-sm" aria-label="Recorded points">
          <thead className="bg-muted text-start text-xs text-muted-foreground">
            <tr>
              <th scope="col" className="px-3 py-2 text-start font-medium">
                Time
              </th>
              <th scope="col" className="px-3 py-2 text-start font-medium">
                Latitude, longitude
              </th>
              <th scope="col" className="px-3 py-2 text-end font-medium">
                Accuracy
              </th>
            </tr>
          </thead>
          <tbody>
            {rows.map((p, i) => (
              <tr key={`${p.t}-${i}`} className="border-t">
                <td className="px-3 py-2 whitespace-nowrap tabular-nums">{formatDateTime(p.t)}</td>
                <td className="px-3 py-2 whitespace-nowrap tabular-nums">
                  {p.lat.toFixed(5)}, {p.lon.toFixed(5)}
                </td>
                <td className="px-3 py-2 text-end whitespace-nowrap tabular-nums">
                  {p.accuracy === null ? '—' : `±${Math.round(p.accuracy)} m`}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      <div className="flex items-center justify-between gap-2 text-sm">
        <Button
          variant="outline"
          className="h-11 px-3"
          disabled={cur === 0}
          onClick={() => setPage(cur - 1)}
        >
          <ChevronLeft aria-hidden="true" />
          Previous
        </Button>
        <span role="status" className="text-muted-foreground tabular-nums">
          Page {cur + 1} of {pages} · {points.length} points
        </span>
        <Button
          variant="outline"
          className="h-11 px-3"
          disabled={cur >= pages - 1}
          onClick={() => setPage(cur + 1)}
        >
          Next
          <ChevronRight aria-hidden="true" />
        </Button>
      </div>
    </div>
  )
}
