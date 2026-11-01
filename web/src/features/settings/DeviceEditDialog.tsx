import { useState, type FormEvent } from 'react'

import { Button } from '@/components/ui/button'
import { FieldError, Input, Label } from '@/components/ui/field'
import { ResponsiveDialog } from '@/components/ui/responsive-dialog'
import { SwitchField } from '@/components/ui/switch-field'
import { DeviceAvatar } from '@/features/devices/DeviceAvatar'
import { commandErrorMessage } from '@/lib/errors'
import type { Device, DevicePatch } from '@/lib/types'
import {
  colorProblem,
  DEVICE_NAME_MAX,
  deviceNameLength,
  deviceNameProblem,
  isHexColor,
  normalizeColor,
  normalizeDeviceName,
} from '@/lib/validation'
import { ColorPicker } from './ColorPicker'
import { useOnce, useUpdateDevice } from './queries'

/** Only the fields that differ from `device` (the server treats an empty name as "reset"). */
export function devicePatch(
  device: Pick<Device, 'device' | 'full_name' | 'color' | 'hidden'>,
  name: string,
  color: string,
  hidden: boolean,
): DevicePatch {
  const patch: DevicePatch = {}
  const clean = normalizeDeviceName(name)
  if ((clean || device.device) !== normalizeDeviceName(device.full_name)) patch.full_name = clean
  if (normalizeColor(color) !== normalizeColor(device.color)) patch.color = normalizeColor(color)
  if (hidden !== device.hidden) patch.hidden = hidden
  return patch
}

function EditForm({
  device,
  update,
  onCancel,
}: {
  device: Device
  update: ReturnType<typeof useUpdateDevice>
  onCancel: () => void
}) {
  // A snapshot: edits are compared with what the dialog opened with.
  const [base] = useState(device)
  const [name, setName] = useState(base.full_name)
  const [color, setColor] = useState(base.color)
  const [hidden, setHidden] = useState(base.hidden)

  const problem = deviceNameProblem(name) ?? colorProblem(color)
  const patch = devicePatch(base, name, color, hidden)
  const dirty = Object.keys(patch).length > 0
  const pending = update.isPending
  const once = useOnce()

  const submit = (e: FormEvent) => {
    e.preventDefault()
    if (problem || !dirty || pending) return
    once((done) => update.mutate(patch, { onSettled: done }))
  }

  const shown = normalizeDeviceName(name) || base.device
  const n = deviceNameLength(name)
  return (
    <form onSubmit={submit} className="flex flex-col gap-4" noValidate>
      <div className="flex items-center gap-3">
        <DeviceAvatar
          deviceKey={base.key}
          name={shown}
          color={isHexColor(color.trim()) ? color.trim() : ''}
        />
        <p
          dir="auto"
          className="min-w-0 truncate text-base font-semibold"
          data-testid="edit-preview"
        >
          {shown}
        </p>
      </div>
      <div className="flex flex-col gap-1.5">
        <Label htmlFor="edit-name">Full name</Label>
        <Input
          id="edit-name"
          dir="auto"
          value={name}
          onChange={(e) => setName(e.target.value)}
          disabled={pending}
          autoComplete="off"
          aria-describedby="edit-name-help"
          aria-invalid={deviceNameProblem(name) ? true : undefined}
        />
        {deviceNameProblem(name) ? (
          <FieldError id="edit-name-error">{deviceNameProblem(name)}</FieldError>
        ) : null}
        <div
          id="edit-name-help"
          className="flex justify-between gap-2 text-xs text-muted-foreground"
        >
          <span>Leave empty to use {base.device}</span>
          <span className={n > DEVICE_NAME_MAX ? 'text-destructive' : undefined}>
            {n}/{DEVICE_NAME_MAX}
          </span>
        </div>
      </div>
      <ColorPicker value={color} onChange={setColor} disabled={pending} />
      <SwitchField
        label="Hidden"
        help="Hidden phones disappear from Cards and Map but keep reporting. You can still see them in Settings."
        checked={hidden}
        onChange={setHidden}
        disabled={pending}
      />
      {update.error ? (
        <FieldError id="edit-error">{commandErrorMessage(update.error)}</FieldError>
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
        <Button type="submit" className="h-11 px-5" disabled={!dirty || !!problem || pending}>
          {pending ? 'Saving…' : 'Save'}
        </Button>
      </div>
    </form>
  )
}

export function DeviceEditDialog({
  device,
  open,
  onOpenChange,
}: {
  device: Device
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  // Owned here so the toast and close still run if the form unmounts mid-request.
  const update = useUpdateDevice(device, () => onOpenChange(false))
  const change = (o: boolean) => {
    if (!o) update.reset()
    onOpenChange(o)
  }
  return (
    <ResponsiveDialog
      open={open}
      onOpenChange={change}
      locked={update.isPending}
      title="Edit device"
      description={`Name and colour for ${device.full_name || device.device} (${device.key}).`}
    >
      <EditForm device={device} update={update} onCancel={() => change(false)} />
    </ResponsiveDialog>
  )
}
