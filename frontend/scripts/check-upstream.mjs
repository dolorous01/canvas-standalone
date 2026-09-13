import { createHash } from 'node:crypto'
import { readdirSync, readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { join, relative, resolve, sep } from 'node:path'

const root = fileURLToPath(new URL('..', import.meta.url))
const expected = '334c9fa27db3efdf22e7b83b1c5d7cc830245e4c6d0df3d042ef823836448554'
const files = []

collect(resolve(root, 'src/upstream'), 'src')
collect(resolve(root, 'public'), 'public')
files.sort((left, right) => left.canonical < right.canonical ? -1 : left.canonical > right.canonical ? 1 : 0)

const aggregate = createHash('sha256')
for (const file of files) {
  const digest = createHash('sha256').update(readFileSync(file.absolute)).digest('hex')
  aggregate.update(`${file.canonical}\0${digest}\n`)
}
const actual = aggregate.digest('hex')
if (actual !== expected) {
  throw new Error(`upstream snapshot hash mismatch: expected ${expected}, got ${actual}`)
}

console.log(`Pinned upstream snapshot verified (${files.length} files, ${actual})`)

function collect(directory, canonicalRoot) {
  for (const entry of readdirSync(directory, { withFileTypes: true })) {
    const absolute = join(directory, entry.name)
    if (entry.isDirectory()) {
      collect(absolute, canonicalRoot)
    } else if (entry.isFile()) {
      const canonical = join(canonicalRoot, relative(resolve(root, canonicalRoot === 'src' ? 'src/upstream' : 'public'), absolute))
        .split(sep)
        .join('/')
      files.push({ absolute, canonical })
    }
  }
}
