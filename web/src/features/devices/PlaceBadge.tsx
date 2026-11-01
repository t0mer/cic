import { House } from 'lucide-react'

import { Badge } from '@/components/ui/badge'

/** Small "Home"-style badge for a phone that is inside a safe place. */
export function PlaceBadge({ name }: { name: string }) {
  return (
    <Badge variant="secondary" data-testid="place-badge" className="max-w-full align-middle">
      <House aria-hidden="true" />
      <span dir="auto" className="truncate">
        {name}
      </span>
    </Badge>
  )
}

/** "Home · " ahead of an address or coordinates; renders nothing without a place. */
export function PlacePrefix({ name }: { name: string | null }) {
  if (!name) return null
  return (
    <>
      <PlaceBadge name={name} />
      <span aria-hidden="true" className="mx-1 text-muted-foreground">
        ·
      </span>
    </>
  )
}
