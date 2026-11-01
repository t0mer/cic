import { colorForKey, readableText } from '@/lib/color'
import { firstGrapheme } from '@/lib/grapheme'
import { cn } from '@/lib/utils'

/** Coloured circle with the first grapheme of the name. Decorative: the name is always next to it. */
export function DeviceAvatar({
  deviceKey,
  name,
  color,
  className,
}: {
  deviceKey: string
  name: string
  color?: string | null
  className?: string
}) {
  const bg = colorForKey(deviceKey, color)
  return (
    <div
      aria-hidden="true"
      data-testid="device-avatar"
      className={cn(
        'flex size-12 shrink-0 items-center justify-center rounded-full text-xl font-semibold',
        className,
      )}
      style={{ backgroundColor: bg, color: readableText(bg) }}
    >
      {firstGrapheme(name)}
    </div>
  )
}
