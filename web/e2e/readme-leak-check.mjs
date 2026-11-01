// Scans the README fixtures/spec (and the README, if present) for real values that must not leak.
// Usage: node e2e/readme-leak-check.mjs   (exits 1 with file:line on a hit)
import { existsSync, readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const here = dirname(fileURLToPath(import.meta.url))
const files = [
  join(here, 'readme-fixtures.ts'),
  join(here, 'readme-shots.spec.ts'),
  join(here, '..', '..', 'README.md'),
].filter(existsSync)

const rules = [
  ['device name', /\b(tomer|sagie)\b/i],
  ['private IPv4', /192\.168\.0\.252/],
  ['API key prefix', /\b(latlng_(?!maps_key)|pk_)[A-Za-z0-9_-]{6,}/],
  ['admin password assignment', /CIC_ADMIN_PASSWORD=\S+/],
  ['email address', /[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}/],
]

let hits = 0
for (const f of files) {
  readFileSync(f, 'utf8')
    .split('\n')
    .forEach((line, i) => {
      for (const [label, re] of rules)
        if (re.test(line)) {
          console.error(`${f}:${i + 1}: ${label}: ${line.trim()}`)
          hits++
        }
    })
}
console.log(`scanned ${files.length} file(s), ${hits} hit(s)`)
process.exit(hits ? 1 : 0)
