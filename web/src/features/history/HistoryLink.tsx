import { History } from 'lucide-react'
import { Link, useInRouterContext } from 'react-router-dom'

import { buttonVariants } from '@/components/ui/button'
import type { Device } from '@/lib/types'
import { cn } from '@/lib/utils'

export const historyPath = (d: Pick<Device, 'prefix' | 'device'>) =>
  `/devices/${encodeURIComponent(d.prefix)}/${encodeURIComponent(d.device)}/history`

/** Entry point to a phone's location history. Callers render it for admins only. */
export function HistoryLink({ device }: { device: Device }) {
  const inRouter = useInRouterContext()
  const name = device.full_name || device.device
  const cls = cn(buttonVariants({ variant: 'outline' }), 'h-11 min-w-11 gap-1.5 px-2')
  const content = (
    <>
      <History aria-hidden="true" />
      History
    </>
  )
  const label = `Location history of ${name}`
  // Some call sites (isolated component tests) have no router; an <a> still works there.
  return inRouter ? (
    <Link to={historyPath(device)} className={cls} aria-label={label}>
      {content}
    </Link>
  ) : (
    <a href={historyPath(device)} className={cls} aria-label={label}>
      {content}
    </a>
  )
}
