import assert from 'node:assert/strict'
import { test } from 'node:test'
import { docsPages, read } from './pages.mjs'

function frontmatter(markdown) {
  const block = markdown.match(/^---\n([\s\S]*?)\n---\n/)?.[1] ?? ''
  const fields = {}
  for (const line of block.split('\n')) {
    const m = line.match(/^([A-Za-z]+):\s*(.*)$/)
    if (!m) continue
    let value = m[2].trim()
    if (value.startsWith("'") && value.endsWith("'")) value = value.slice(1, -1).replaceAll("''", "'")
    else if (value.startsWith('"') && value.endsWith('"')) value = JSON.parse(value)
    fields[m[1]] = value
  }
  return fields
}

const pages = docsPages().map((page) => ({ page, ...frontmatter(read(page)) }))

for (const { page, description } of pages) {
  test(`${page} has a description of 120 to 160 characters`, () => {
    assert.ok(description, 'description in the frontmatter')
    assert.ok(
      description.length >= 120 && description.length <= 160,
      `description is ${description.length} characters`,
    )
  })
}

test('no two pages share a description', () => {
  const seen = new Map()
  for (const { page, description } of pages) {
    if (!description) continue
    assert.ok(!seen.has(description), `${page} repeats the description of ${seen.get(description)}`)
    seen.set(description, page)
  }
})
