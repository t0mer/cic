import { Pencil, Plus, Trash2 } from 'lucide-react'
import { useState } from 'react'

import { Button } from '@/components/ui/button'
import { formatCoords } from '@/lib/maps'
import type { Place } from '@/lib/types'
import { PlaceDeleteDialog } from './PlaceDeleteDialog'
import { PlaceDialog } from './PlaceDialog'
import { usePlaces } from './queries'
import { QueryState } from './QueryState'

const byName = (a: Place, b: Place) =>
  a.name.localeCompare(b.name, undefined, { sensitivity: 'base', numeric: true }) || a.id - b.id

function PlaceRow({ place }: { place: Place }) {
  const [editing, setEditing] = useState(false)
  const [deleting, setDeleting] = useState(false)
  return (
    <li
      className="flex flex-col gap-3 rounded-lg border p-3 sm:flex-row sm:items-center"
      data-testid="settings-place-row"
    >
      <div className="min-w-0 flex-1">
        <p dir="auto" className="truncate font-medium">
          {place.name}
        </p>
        <p className="text-xs text-muted-foreground">
          {formatCoords(place.latitude, place.longitude)} · {place.radius_m} m radius
        </p>
      </div>
      <div className="flex gap-1">
        <Button
          variant="outline"
          className="h-11 px-4"
          aria-label={`Edit ${place.name}`}
          onClick={() => setEditing(true)}
        >
          <Pencil aria-hidden="true" />
          Edit
        </Button>
        <Button
          variant="outline"
          className="h-11 px-4 text-destructive"
          aria-label={`Delete ${place.name}`}
          onClick={() => setDeleting(true)}
        >
          <Trash2 aria-hidden="true" />
          Delete
        </Button>
      </div>
      {editing ? <PlaceDialog place={place} open onOpenChange={setEditing} /> : null}
      <PlaceDeleteDialog place={place} open={deleting} onOpenChange={setDeleting} />
    </li>
  )
}

export function PlacesSection() {
  const places = usePlaces()
  const [adding, setAdding] = useState(false)
  return (
    <section aria-labelledby="places-h" className="flex flex-col gap-4">
      <div className="flex items-start justify-between gap-3">
        <div>
          <h2 id="places-h" className="text-lg font-semibold">
            Safe places
          </h2>
          <p className="text-sm text-muted-foreground">
            Named circles such as Home or Work. A phone inside one shows that name next to its
            address.
          </p>
        </div>
        <Button className="h-11 shrink-0 px-4" onClick={() => setAdding(true)}>
          <Plus aria-hidden="true" />
          Add place
        </Button>
      </div>
      <QueryState query={places} what="safe places" empty="No safe places yet">
        {(list) => (
          <ul className="flex flex-col gap-2">
            {[...list].sort(byName).map((p) => (
              <PlaceRow key={p.id} place={p} />
            ))}
          </ul>
        )}
      </QueryState>
      {adding ? <PlaceDialog place={null} open onOpenChange={setAdding} /> : null}
    </section>
  )
}
