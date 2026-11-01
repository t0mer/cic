import { useId } from 'react'

import { Button } from '@/components/ui/button'
import { FieldError, Input, Label } from '@/components/ui/field'
import { PRESETS, PRESET_LABELS, type Preset, type ResolvedRange } from '@/lib/history-range'

export function RangeFilter({
  range,
  onFrom,
  onTo,
  onPreset,
}: {
  range: ResolvedRange
  onFrom: (v: string) => void
  onTo: (v: string) => void
  onPreset: (p: Preset) => void
}) {
  const id = useId()
  const errId = `${id}-err`
  return (
    <form
      className="flex flex-col gap-3 rounded-xl border bg-card p-3"
      aria-label="Date range"
      onSubmit={(e) => e.preventDefault()}
    >
      <div className="flex flex-wrap gap-2" role="group" aria-label="Quick ranges">
        {PRESETS.map((p) => (
          <Button
            key={p}
            type="button"
            variant={range.preset === p ? 'default' : 'outline'}
            className="h-11 px-4"
            aria-pressed={range.preset === p}
            onClick={() => onPreset(p)}
          >
            {PRESET_LABELS[p]}
          </Button>
        ))}
      </div>
      <div className="grid gap-3 sm:grid-cols-2">
        <div className="flex min-w-0 flex-col gap-1">
          <Label htmlFor={`${id}-from`}>From</Label>
          <Input
            id={`${id}-from`}
            type="datetime-local"
            value={range.fromInput}
            onChange={(e) => onFrom(e.target.value)}
            aria-invalid={range.error ? true : undefined}
            aria-describedby={range.error ? errId : undefined}
          />
        </div>
        <div className="flex min-w-0 flex-col gap-1">
          <Label htmlFor={`${id}-to`}>To</Label>
          <Input
            id={`${id}-to`}
            type="datetime-local"
            value={range.toInput}
            onChange={(e) => onTo(e.target.value)}
            aria-invalid={range.error ? true : undefined}
            aria-describedby={range.error ? errId : undefined}
          />
        </div>
      </div>
      {range.error ? <FieldError id={errId}>{range.error}</FieldError> : null}
    </form>
  )
}
