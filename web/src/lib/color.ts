function hash(s: string): number {
  let h = 2166136261
  for (let i = 0; i < s.length; i++) {
    h ^= s.charCodeAt(i)
    h = Math.imul(h, 16777619)
  }
  return h >>> 0
}

const HEX = /^#(?:[0-9a-f]{3}|[0-9a-f]{6})$/i

/** Dark enough that white text keeps AA contrast (lightness 32%). */
export function colorForKey(key: string, explicit?: string | null): string {
  if (explicit && HEX.test(explicit.trim())) return explicit.trim()
  return `hsl(${hash(key) % 360} 55% 32%)`
}

/** Black or white, whichever has the higher WCAG contrast ratio on the given #hex color (white for anything else). */
export function readableText(bg: string): string {
  const m = /^#([0-9a-f]{3}|[0-9a-f]{6})$/i.exec(bg)
  if (!m) return '#ffffff'
  let hex = m[1]
  if (hex.length === 3)
    hex = hex
      .split('')
      .map((c) => c + c)
      .join('')
  const [r, g, b] = [0, 2, 4].map((i) => {
    const v = parseInt(hex.slice(i, i + 2), 16) / 255
    return v <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4
  })
  const lum = 0.2126 * r + 0.7152 * g + 0.0722 * b
  const whiteContrast = 1.05 / (lum + 0.05)
  const blackContrast = (lum + 0.05) / 0.05
  return blackContrast > whiteContrast ? '#000000' : '#ffffff'
}
