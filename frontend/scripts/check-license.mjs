import { existsSync, readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { resolve } from 'node:path'

const root = fileURLToPath(new URL('..', import.meta.url))
const required = [
  'LICENSE',
  'LICENSE.upstream',
  'NOTICE',
  'UPSTREAM.md',
  '../dist/manifest.json',
  '../dist/LICENSE',
  '../dist/LICENSE.upstream',
  '../dist/NOTICE'
]

for (const relative of required) {
  if (!existsSync(resolve(root, relative))) {
    throw new Error(`missing required canvas release file: ${relative}`)
  }
}

const sourceURL = process.env.VITE_CANVAS_SOURCE_URL || ''
if (!sourceURL.startsWith('https://')) {
  throw new Error('VITE_CANVAS_SOURCE_URL must be an HTTPS corresponding-source URL')
}

const license = readFileSync(resolve(root, 'LICENSE'), 'utf8')
if (!license.includes('GNU AFFERO GENERAL PUBLIC LICENSE')) {
  throw new Error('canvas LICENSE is not AGPL-3.0')
}

const upstreamLicense = readFileSync(resolve(root, 'LICENSE.upstream'), 'utf8')
if (!upstreamLicense.includes('MIT License') || !upstreamLicense.includes('Copyright (c) 2026 basketikun')) {
  throw new Error('canvas upstream LICENSE is not the pinned MIT license')
}

const pinnedCommit = '9414048f9d0a099386aa15d81bedb5376b79ee61'
const upstream = readFileSync(resolve(root, 'UPSTREAM.md'), 'utf8')
const notice = readFileSync(resolve(root, 'NOTICE'), 'utf8')
if (!upstream.includes('v0.16.0') || !upstream.includes(pinnedCommit) || !upstream.includes('License: MIT')) {
  throw new Error('canvas UPSTREAM.md does not describe the pinned MIT snapshot')
}
if (!notice.includes('v0.16.0') || !notice.includes(pinnedCommit) || !notice.includes('MIT License') || !notice.includes('AGPL-3.0-only')) {
  throw new Error('canvas NOTICE does not describe the pinned snapshot and integration licenses')
}

const manifest = JSON.parse(readFileSync(resolve(root, '../dist/manifest.json'), 'utf8'))
const entries = Object.values(manifest)
if (!entries.some((entry) => entry && entry.isEntry && typeof entry.file === 'string')) {
  throw new Error('canvas manifest has no ESM entry')
}

console.log(`Infinite Canvas release metadata verified (${sourceURL})`)
