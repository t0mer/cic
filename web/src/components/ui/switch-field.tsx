import { useId, type ReactNode } from 'react'

interface FieldProps {
  label: string
  help?: ReactNode
  checked: boolean
  onChange: (v: boolean) => void
  disabled?: boolean
}

/**
 * Native checkbox inside its label: the whole 44 px row is the click target. The visual box stays
 * small; the focus ring is drawn around the row.
 */
function ToggleField({
  label,
  help,
  checked,
  onChange,
  disabled,
  role,
}: FieldProps & { role?: 'switch' }) {
  const id = useId()
  return (
    <div className="flex flex-col">
      <label className="flex min-h-11 w-full cursor-pointer items-center gap-3 rounded-md text-sm font-medium has-[:disabled]:cursor-not-allowed has-[:disabled]:opacity-60 has-[:focus-visible]:ring-[3px] has-[:focus-visible]:ring-ring">
        <input
          type="checkbox"
          role={role}
          checked={checked}
          disabled={disabled}
          aria-describedby={help ? `${id}-help` : undefined}
          onChange={(e) => onChange(e.target.checked)}
          className="size-6 shrink-0 accent-primary focus-visible:outline-none"
        />
        <span>{label}</span>
      </label>
      {help ? (
        <p id={`${id}-help`} className="text-xs text-muted-foreground">
          {help}
        </p>
      ) : null}
    </div>
  )
}

/** Labelled on/off switch: a native checkbox with the switch role. */
export const SwitchField = (p: FieldProps) => <ToggleField {...p} role="switch" />

/** Labelled checkbox with the same 44 px hit area. */
export const CheckboxField = (p: FieldProps) => <ToggleField {...p} />
