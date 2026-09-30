import assert from 'node:assert/strict'
import { readdirSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { test } from 'node:test'
import { docsPages, docsRoot, read } from './pages.mjs'

const dir = join(docsRoot, 'public', 'screenshots')
const files = readdirSync(dir).filter((name) => !name.startsWith('.'))

function pngSize(name) {
  const png = readFileSync(join(dir, name))
  assert.equal(png.toString('latin1', 1, 4), 'PNG', `${name} is a PNG`)
  return { width: png.readUInt32BE(16), height: png.readUInt32BE(20) }
}

const uses = docsPages().flatMap((page) =>
  [...read(page).matchAll(/<Screenshot\b([\s\S]*?)\/>/g)].map((m) => {
    const attr = (key) => m[1].match(new RegExp(`(?:^|\\s):?${key}="([^"]*)"`))?.[1]
    return { page, name: attr('name'), alt: attr('alt'), width: Number(attr('width')), height: Number(attr('height')) }
  }),
)

test('pages use screenshots', () => {
  assert.ok(uses.length > 0)
})

for (const { page, name, alt, width, height } of uses) {
  test(`${page}: screenshot ${name} has both schemes, alt text and its size`, () => {
    assert.ok(alt && alt.length >= 40, 'descriptive alt text')
    for (const scheme of ['light', 'dark']) {
      const file = `${name}-${scheme}.png`
      assert.ok(files.includes(file), `${file} exists`)
      const size = pngSize(file)
      assert.equal(size.width, width * 2, `${file} is ${width} CSS px wide at 2x`)
      assert.ok(Math.abs(size.height - height * 2) <= 2, `${file} is ${height} CSS px high at 2x`)
    }
  })
}

test('every screenshot file is used by a page', () => {
  const used = new Set(uses.flatMap(({ name }) => [`${name}-light.png`, `${name}-dark.png`]))
  assert.deepEqual(files.filter((file) => !used.has(file)), [])
})
