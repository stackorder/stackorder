import assert from 'node:assert/strict'
import { readdirSync } from 'node:fs'
import { join, relative } from 'node:path'
import { test } from 'node:test'
import { read, repoRoot } from './pages.mjs'

const moduleRef = /github\.com\/stackorder\/stackorder\/\/deploy\/terraform\?ref=v([^"\s]+)/g
const imageTag = /image_tag\s*=\s*"([^"]+)"/g

const pins = {
  'README.md': [
    moduleRef,
    imageTag,
    /^> \*\*Status:\*\* v([0-9]+(?:\.[0-9]+)*)/gm,
    /gh release download v(\S+)/g,
    /stackorder_([0-9][^_]*)_(?:linux|darwin|windows|checksums)/g,
    /uses: stackorder\/actions\/setup@v1\n\s+with:\n\s+version: (\S+)/g,
    /ghcr\.io\/stackorder\/stackorder:([0-9][^`\s]*)/g,
    [/\(also `:([0-9][^`]*)` and `:latest`\)/g, (version) => version.split('.').slice(0, 2).join('.')],
  ],
  'deploy/terraform/README.md': [moduleRef, imageTag],
  'deploy/terraform/examples/self-hosted/README.md': [moduleRef],
  'deploy/terraform/examples/self-hosted/variables.tf': [
    /variable "stackorder_version" \{[^}]*?default\s*=\s*"([^"]+)"/g,
  ],
  'docs/operations/deploy-aws.md': [moduleRef, imageTag],
  'docs/index.md': [/Current release: v([0-9]+(?:\.[0-9]+)*)/g],
  'docs/guide/local-demo.md': [
    /releases\/download\/v(\S+)/g,
    /stackorder_([0-9][^_]*)_/g,
    /"version":"([0-9][^"]*)"/g,
  ],
}

function latestRelease() {
  const m = read('CHANGELOG.md').match(/^## \[(\d+\.\d+\.\d+)\] - \d{4}-\d{2}-\d{2}$/m)
  assert.ok(m, 'CHANGELOG.md has no dated release heading')
  return m[1]
}

function filesWithModuleRef() {
  const found = []
  const walk = (dir) => {
    for (const entry of readdirSync(dir, { withFileTypes: true })) {
      if (entry.name.startsWith('.') || entry.name === 'node_modules' || entry.name === 'dist') continue
      const path = join(dir, entry.name)
      if (entry.isDirectory()) walk(path)
      else if (/\.(md|tf)$/.test(entry.name)) {
        const file = relative(repoRoot, path)
        if (new RegExp(moduleRef.source).test(read(file))) found.push(file)
      }
    }
  }
  walk(repoRoot)
  return found.sort()
}

test('every version pin carries the latest release in CHANGELOG.md', () => {
  const version = latestRelease()
  for (const [file, patterns] of Object.entries(pins)) {
    const text = read(file)
    for (const entry of patterns) {
      const [pattern, expect] = Array.isArray(entry) ? entry : [entry, (v) => v]
      const want = expect(version)
      const matches = [...text.matchAll(pattern)]
      assert.ok(matches.length > 0, `${file}: no pin matches ${pattern}`)
      for (const m of matches) {
        assert.equal(m[1], want, `${file}: "${m[0]}" does not match ${version}, the latest release in CHANGELOG.md`)
      }
    }
  }
})

test('every file that pins the module is checked', () => {
  const checked = Object.entries(pins)
    .filter(([, patterns]) => patterns.includes(moduleRef))
    .map(([file]) => file)
    .sort()
  assert.deepEqual(filesWithModuleRef(), checked)
})
