import assert from 'node:assert/strict'
import { test } from 'node:test'
import { read } from './pages.mjs'

const changelog = read('CHANGELOG.md')

function unreleased() {
  const text = read('CHANGELOG.md')
  const start = text.indexOf('## [Unreleased]')
  assert.ok(start >= 0, 'CHANGELOG.md has an Unreleased section')
  const after = text.indexOf('\n## [', start + 1)
  return text.slice(start, after < 0 ? text.length : after)
}

test('no changelog bullet is repeated verbatim', () => {
  const seen = new Set()
  for (const line of changelog.split('\n')) {
    if (!line.startsWith('- ')) continue
    assert.ok(!seen.has(line), `repeated changelog entry: ${line}`)
    seen.add(line)
  }
})

test('the GitHub-retry idempotency fix has exactly one Unreleased entry', () => {
  const entries = unreleased()
    .split('\n')
    .filter((line) => line.startsWith('- The server no longer retries a GitHub call'))
  assert.equal(
    entries.length,
    1,
    `expected one entry for this change, found ${entries.length}`,
  )
})
