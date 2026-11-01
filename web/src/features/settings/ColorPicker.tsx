import { Check } from 'lucide-react'
import { useId } from 'react'

import { FieldError, Input, Label } from '@/components/ui/field'
import { readableText } from '@/lib/color'
import { colorProblem, normalizeColor } from '@/lib/validation'
import { cn } from '@/lib/utils'

/** Every swatch keeps white text above WCAG AA contrast. */
export const PALETTE = [
  { name: 'Red', hex: '#b91c1c' },
  { name: 'Orange', hex: '#c2410c' },
  { name: 'Amber', hex: '#a16207' },
  { name: 'Green', hex: '#15803d' },
  { name: 'Teal', hex: '#0f766e' },
  { name: 'Sky', hex: '#0369a1' },
  { name: 'Blue', hex: '#1d4ed8' },
  { name: 'Violet', hex: '#6d28d9' },
  { name: 'Pink', hex: '#be185d' },
  { name: 'Slate', hex: '#475569' },
] as const

const ring = 'focus-visible:ring-[3px] focus-visible:ring-ring focus-visible:outline-none'

/** Swatches, an "Automatic" option and a validated hex field. `value` is '' (automatic) or text. */
export function ColorPicker({
  value,
  onChange,
  disabled,
}: {
  value: string
  onChange: (v: string) => void
  disabled?: boolean
}) {
  const id = useId()
  const norm = normalizeColor(value)
  const problem = colorProblem(value)
  return (
    <div className="flex flex-col gap-2" role="group" aria-labelledby={`${id}-label`}>
      <Label id={`${id}-label`}>Colour</Label>
      <div className="flex flex-wrap gap-1">
        <button
          type="button"
          disabled={disabled}
          aria-pressed={norm === ''}
          onClick={() => onChange('')}
          className={cn(
            'inline-flex h-11 items-center rounded-md border px-3 text-sm font-medium hover:bg-accent disabled:opacity-50',
            norm === '' && 'border-ring bg-accent',
            ring,
          )}
        >
          Automatic
        </button>
        {PALETTE.map((c) => {
          const on = norm === c.hex
          return (
            <button
              key={c.hex}
              type="button"
              disabled={disabled}
              aria-pressed={on}
              aria-label={`${c.name} ${c.hex}`}
              title={c.name}
              onClick={() => onChange(c.hex)}
              style={{ backgroundColor: c.hex, color: readableText(c.hex) }}
              className={cn(
                'inline-flex size-11 items-center justify-center rounded-md border-2 border-transparent disabled:opacity-50',
                on && 'border-foreground',
                ring,
              )}
            >
              {on ? <Check className="size-5" aria-hidden="true" /> : null}
            </button>
          )
        })}
      </div>
      <Input
        id={`${id}-hex`}
        aria-label="Hex colour"
        value={value}
        onChange={(e) => onChange(e.target.value)}
        disabled={disabled}
        placeholder="#1a73e8"
        autoComplete="off"
        spellCheck={false}
        maxLength={7}
        className="font-mono"
        aria-invalid={problem ? true : undefined}
        aria-describedby={problem ? `${id}-err` : undefined}
      />
      {problem ? <FieldError id={`${id}-err`}>{problem}</FieldError> : null}
    </div>
  )
}
