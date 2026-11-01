import { useState, type FormEvent } from 'react'

import { Button } from '@/components/ui/button'
import { FieldError, Input, Label } from '@/components/ui/field'
import { ResponsiveDialog } from '@/components/ui/responsive-dialog'
import { commandErrorMessage } from '@/lib/errors'
import { hasLocation } from '@/lib/location'
import type { Place, PlaceInput } from '@/lib/types'
import {
  latitudeProblem,
  longitudeProblem,
  normalizeDeviceName,
  parseNumber,
  placeNameProblem,
  PLACE_NAME_MAX,
  PLACE_RADIUS_MAX,
  PLACE_RADIUS_MIN,
  radiusProblem,
} from '@/lib/validation'
import { useAllDevices, useOnce, useSavePlace } from './queries'

/** The request body: everything for a new place, only the changed fields when editing. */
export function placeBody(
  base: Place | null,
  f: { name: string; latitude: string; longitude: string; radius: string },
): PlaceInput | Partial<PlaceInput> {
  const next: PlaceInput = {
    name: normalizeDeviceName(f.name),
    latitude: parseNumber(f.latitude),
    longitude: parseNumber(f.longitude),
    radius_m: parseNumber(f.radius),
  }
  if (!base) return next
  const patch: Partial<PlaceInput> = {}
  if (next.name !== base.name) patch.name = next.name
  if (next.latitude !== base.latitude) patch.latitude = next.latitude
  if (next.longitude !== base.longitude) patch.longitude = next.longitude
  if (next.radius_m !== base.radius_m) patch.radius_m = next.radius_m
  return patch
}

const round6 = (n: number) => String(Math.round(n * 1e6) / 1e6)

function Field({
  id,
  label,
  problem,
  help,
  children,
}: {
  id: string
  label: string
  problem: string | null
  help?: string
  children: React.ReactNode
}) {
  return (
    <div className="flex flex-col gap-1.5">
      <Label htmlFor={id}>{label}</Label>
      {children}
      {problem ? <FieldError id={`${id}-error`}>{problem}</FieldError> : null}
      {help && !problem ? <p className="text-xs text-muted-foreground">{help}</p> : null}
    </div>
  )
}

function PlaceForm({
  base,
  save,
  onCancel,
}: {
  base: Place | null
  save: ReturnType<typeof useSavePlace>
  onCancel: () => void
}) {
  const [name, setName] = useState(base?.name ?? '')
  const [lat, setLat] = useState(base ? String(base.latitude) : '')
  const [lon, setLon] = useState(base ? String(base.longitude) : '')
  const [radius, setRadius] = useState(base ? String(base.radius_m) : '100')
  const [touched, setTouched] = useState(false)
  const devices = useAllDevices()
  const located = (devices.data ?? []).filter(hasLocation)
  const once = useOnce()
  const pending = save.isPending

  const problems = {
    name: placeNameProblem(name),
    lat: latitudeProblem(lat),
    lon: longitudeProblem(lon),
    radius: radiusProblem(radius),
  }
  const invalid = Object.values(problems).some(Boolean)
  const body = placeBody(base, { name, latitude: lat, longitude: lon, radius })
  const dirty = !base || Object.keys(body).length > 0
  // Problems are shown once the user has tried to save, or for a field they have filled in.
  const show = (v: string, p: string | null) => (touched || v.trim() !== '' ? p : null)

  const submit = (e: FormEvent) => {
    e.preventDefault()
    setTouched(true)
    if (invalid || !dirty || pending) return
    once((done) => save.mutate(body, { onSettled: done }))
  }

  const fromPhone = (key: string) => {
    const d = located.find((x) => x.key === key)
    if (!d || d.latitude === null || d.longitude === null) return
    setLat(round6(d.latitude))
    setLon(round6(d.longitude))
  }

  return (
    <form onSubmit={submit} className="flex flex-col gap-4" noValidate>
      <Field id="place-name" label="Name" problem={show(name, problems.name)}>
        <Input
          id="place-name"
          dir="auto"
          value={name}
          onChange={(e) => setName(e.target.value)}
          disabled={pending}
          autoComplete="off"
          maxLength={PLACE_NAME_MAX * 2}
          placeholder="Home"
          aria-invalid={show(name, problems.name) ? true : undefined}
        />
      </Field>
      <div className="flex flex-col gap-1.5">
        <Label htmlFor="place-phone">Use a phone&apos;s location</Label>
        <select
          id="place-phone"
          value=""
          onChange={(e) => fromPhone(e.target.value)}
          disabled={pending || located.length === 0}
          className="h-11 w-full rounded-md border border-input bg-background px-3 text-base disabled:opacity-50"
        >
          <option value="">
            {located.length === 0 ? 'No phone has a location yet' : 'Choose a phone…'}
          </option>
          {located.map((d) => (
            <option key={d.key} value={d.key}>
              {d.full_name || d.device}
            </option>
          ))}
        </select>
        <p className="text-xs text-muted-foreground">Fills in latitude and longitude below.</p>
      </div>
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
        <Field id="place-lat" label="Latitude" problem={show(lat, problems.lat)}>
          <Input
            id="place-lat"
            inputMode="decimal"
            value={lat}
            onChange={(e) => setLat(e.target.value)}
            disabled={pending}
            autoComplete="off"
            aria-invalid={show(lat, problems.lat) ? true : undefined}
          />
        </Field>
        <Field id="place-lon" label="Longitude" problem={show(lon, problems.lon)}>
          <Input
            id="place-lon"
            inputMode="decimal"
            value={lon}
            onChange={(e) => setLon(e.target.value)}
            disabled={pending}
            autoComplete="off"
            aria-invalid={show(lon, problems.lon) ? true : undefined}
          />
        </Field>
      </div>
      <Field
        id="place-radius"
        label="Radius (metres)"
        problem={show(radius, problems.radius)}
        help={`${PLACE_RADIUS_MIN} to ${PLACE_RADIUS_MAX}. A phone inside this circle is shown as being at this place.`}
      >
        <Input
          id="place-radius"
          inputMode="numeric"
          value={radius}
          onChange={(e) => setRadius(e.target.value)}
          disabled={pending}
          autoComplete="off"
          aria-invalid={show(radius, problems.radius) ? true : undefined}
        />
      </Field>
      {save.error ? (
        <FieldError id="place-error">{commandErrorMessage(save.error)}</FieldError>
      ) : null}
      <div className="flex flex-col-reverse gap-2 sm:flex-row sm:justify-end">
        <Button
          type="button"
          variant="outline"
          className="h-11 px-5"
          onClick={onCancel}
          disabled={pending}
        >
          Cancel
        </Button>
        <Button type="submit" className="h-11 px-5" disabled={!dirty || pending}>
          {pending ? 'Saving…' : 'Save'}
        </Button>
      </div>
    </form>
  )
}

/** Add (`place` null) or edit a safe place. */
export function PlaceDialog({
  place,
  open,
  onOpenChange,
}: {
  place: Place | null
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const save = useSavePlace(place, () => onOpenChange(false))
  const change = (o: boolean) => {
    if (!o) save.reset()
    onOpenChange(o)
  }
  return (
    <ResponsiveDialog
      open={open}
      onOpenChange={change}
      locked={save.isPending}
      title={place ? 'Edit safe place' : 'Add safe place'}
      description="A named circle on the map. Phones inside it show the place name."
    >
      <PlaceForm base={place} save={save} onCancel={() => change(false)} />
    </ResponsiveDialog>
  )
}
