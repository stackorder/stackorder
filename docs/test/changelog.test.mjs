import assert from 'node:assert/strict'
import { test } from 'node:test'
import { read } from './pages.mjs'

const changelog = read('CHANGELOG.md')

test('no changelog bullet is repeated verbatim', () => {
  const seen = new Set()
  for (const line of changelog.split('\n')) {
    if (!line.startsWith('- ')) continue
    assert.ok(!seen.has(line), `repeated changelog entry: ${line}`)
    seen.add(line)
  }
})
