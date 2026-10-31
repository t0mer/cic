const segmenter =
  typeof Intl !== 'undefined' && 'Segmenter' in Intl
    ? new Intl.Segmenter(undefined, { granularity: 'grapheme' })
    : null

/** First user-perceived character of a name, upper-cased where the script has case. */
export function firstGrapheme(name: string): string {
  const s = name.trim()
  if (!s) return '?'
  const first = segmenter
    ? (segmenter.segment(s)[Symbol.iterator]().next().value?.segment ?? '')
    : (Array.from(s)[0] ?? '')
  return (first || '?').toLocaleUpperCase()
}
